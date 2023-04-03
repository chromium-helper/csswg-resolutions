package triage_task_handler

import (
	"context"
	"fmt"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	gcpsm "cloud.google.com/go/secretmanager/apiv1"
	gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/chromium-helper/csswg-resolutions/fsresolutions"
	"github.com/chromium-helper/csswg-resolutions/monorail"
	"github.com/google/go-github/github"
)

var (
	gcpProjectId          = os.Getenv("GCP_PROJECT_ID")
	gcpGithubAPIKeySecret = os.Getenv("GCP_GITHUB_API_KEY_SECRET_NAME")
	gcpFsCollection       = os.Getenv("GCP_FS_COLLECTION")
	githubLogin           = os.Getenv("GITHUB_LOGIN")
	githubRepo            = os.Getenv("GITHUB_REPO")
	componentLabelPrefix  = os.Getenv("COMPONENT_LABEL_PREFIX")
	metaBugLabel          = os.Getenv("META_BUG_LABEL")
)

type App struct {
	FSClient     *fsresolutions.Client
	GithubClient *github.Client
}

type Directive struct {
	Components []string
	Crbug int
	Owner string
	CcList []string
	Commenter string
	Comment string
}

func NewApp() (*App, error) {
	fsclient, err := fsresolutions.NewClient(gcpProjectId, gcpFsCollection)
	if err != nil {
		return nil, fmt.Errorf("fsresolutions.NewClient: %v", err)
	}

	return &App{
		FSClient: fsclient,
	}, nil
}

func GetGithubAPIToken(ctx context.Context) (string, error) {
	client, err := gcpsm.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("gcpsm.NewClient: %v", err)
	}
	defer client.Close()

	req := &gcpsmpb.AccessSecretVersionRequest{
		Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest",
			gcpProjectId,
			gcpGithubAPIKeySecret,
		),
	}

	secret, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("gcpsm.AccessSecretVersion: %v\n", err)
	}
	return string(secret.Payload.GetData()), nil
}

func NewGithubClient() (*github.Client, error) {
	ctx := context.Background()
	token, err := GetGithubAPIToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetGithubAPIToken: %v", err)
	}

	token_source := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token})
	token_client := oauth2.NewClient(ctx, token_source)
	return github.NewClient(token_client), nil
}

func fileNameFromData(fsdata *fsresolutions.FSResolutionData) string {
	return fmt.Sprintf("%d", fsdata.CsswgDraftsId)
}

func (app *App) UpdateMonorailIssue(ghissue *github.Issue, directive *Directive) (*monorail.Issue, error) {
	audience, err := monorail.GetAudience("prod")
	if err != nil {
		return nil, fmt.Errorf("GetAudience: %v", err)
	}

	ctx := context.Background()
	token_source, err := idtoken.NewTokenSource(ctx, audience)
	if err != nil {
		return nil, fmt.Errorf("NewTokenSource: %v", err)
	}

	service, err := monorail.NewIssuesService(ctx, "prod", token_source)
	if err != nil {
		return nil, fmt.Errorf("monorail.NewIssuesService: %v", err)
	}

	description := ghissue.GetBody()
	description += "\n\n"
	if len(directive.Comment) != 0 {
		description += fmt.Sprintf("%s left an additional comment:\n%s\n\n", directive.Commenter, directive.Comment)
	}
	description += fmt.Sprintf("This issue has been triaged via https://github.com/chromium-helper/csswg-resolutions/issues/%d\n", ghissue.GetNumber())

	if directive.Crbug == 0 {
		description += "If no action is needed, feel free to close this bug. Otherwise, please prioritize the work needed for the above resolutions."
		description += "\n\n"

		request := &monorail.CreateIssueRequest{
			Project:     "chromium",
			Summary:     ghissue.GetTitle(),
			Description: description,
			Components:  directive.Components,
			Owner:			 directive.Owner,
			CcList:      directive.CcList,
		}
		issue, err := service.CreateIssue(request)
		if err != nil {
			return nil, fmt.Errorf("monorail.CreateIssue: %v", err)
		}
	} else {
	}
	return issue, nil
}

func (app *App) CommentAndClose(ghissue *github.Issue, crbug_id int) error {
	ctx := context.Background()

	// Add a comment
	comment_text := fmt.Sprintf("I have filed [crbug.com/%d](https://crbug.com/%d)\n\n", crbug_id, crbug_id)
	comment_text += "That is all that can be done here, closing issue."
	comment := &github.IssueComment{Body: &comment_text}
	_, _, err := app.GithubClient.Issues.CreateComment(
		ctx, githubLogin, githubRepo, ghissue.GetNumber(), comment)
	if err != nil {
		return fmt.Errorf("Issues.CreateComment: %v", err)
	}

	// Close the issue.
	new_state := "closed"
	close_request := &github.IssueRequest{State: &new_state}
	_, _, err = app.GithubClient.Issues.Edit(
		ctx, githubLogin, githubRepo, ghissue.GetNumber(), close_request)
	if err != nil {
		return fmt.Errorf("Issues.Edit: %v", err)
	}
	return nil
}

func ParseComponents(issue *github.Issue) {
	var components []string
	for _, label := range issue.Labels {
		if strings.HasPrefix(label.GetName(), componentLabelPrefix) {
			components = append(components, label.GetName()[len(componentLabelPrefix):])
		}

		// We should still verify meta labels
		if label.GetName() == metaBugLabel {
			return nil, true
		}
	}
	return components, false
}

func (app *App) ProcessIssue(fsdata *fsresolutions.FSResolutionData) error {
	// We already have a bug filed (TODO: maybe we need to add a comment?)
	if fsdata.CrbugId != 0 {
		return nil
	}

	ctx := context.Background()
	issue, _, err := app.GithubClient.Issues.Get(ctx, githubLogin, githubRepo, fsdata.CsswgResolutionsId)
	if err != nil {
		return fmt.Errorf("Issues.Get: %v", err)
	}

	// Issue is closed, don't process it.
	if issue.GetState() == "closed" {
		return nil
	}

	components, skip := ParseComponents(issue)
	if skip {
		return nil
	}

	collaborators, _, err := app.GithubClient.Repositories.ListCollaborators(ctx, githubLogin, githubRepo)
	if err != nil {
		return fmt.Errorf("ListCollaborators: %v")
	}

	directive, err := ParseDirective(issue, collaborators)
	if err != nil {
		return fmt.Errorf("ParseDirectives: %v")
	}

	// We need a component or a crbug
	if len(directive.Components) == 0 && directive.Crbug == 0 {
		return nil
	}

	crbug, err := app.UpdateMonorailIssue(issue, directive)
	if err != nil {
		return fmt.Errorf("UpdateMonorailIssue: %v", err)
	}

	var action string
	if directive.Crbug == 0 {
		action = "filed"
	} else {
		action = "left a comment on"
	}

	fsdata.CrbugId = crbug.Id
	err = app.CommentAndClose(action, issue, crbug.Id)
	if err != nil {
		return fmt.Errorf("CommentAndClose: %v", err)
	}
	return nil
}

func (app *App) UpdateFsDataAndClose(fsdata *fsresolutions.FSResolutionData) {
	err := app.FSClient.SetData(fileNameFromData(fsdata), fsdata)
	// TODO: Rework this to return the value instead of panicking.
	if err != nil {
		panic(err)
	}
	app.FSClient.Close()
}

func (app *App) Run(csswg_resolutions_id int) error {
	fsdata, err := app.FSClient.LoadDataByCsswgResolutionsId(csswg_resolutions_id)
	if err != nil {
		return fmt.Errorf("LoadDataByCsswgResolutionsId: %v", err)
	}
	fsdata.HasPendingTriageEvents = false
	defer app.UpdateFsDataAndClose(fsdata)

	githubClient, err := NewGithubClient()
	if err != nil {
		return fmt.Errorf("NewGithubClient: %v", err)
	}
	app.GithubClient = githubClient
	err = app.ProcessIssue(fsdata)
	return err
}

func HandleQueueTask(w http.ResponseWriter, r *http.Request) {
	err := r.ParseForm()
	if err != nil {
		log.Printf("ERROR: ParseForm: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	csswg_resolutions_id, err := strconv.Atoi(r.FormValue("CsswgResolutionsId"))
	if err != nil {
		log.Printf("ERROR: Unexpected atoi %s: %v\n", r.FormValue("CsswgResolutionsId"), err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	log.Printf("Processing csswg resolutions issue %d\n", csswg_resolutions_id)
	w.WriteHeader(http.StatusOK)

	app, err := NewApp()
	if err != nil {
		log.Printf("ERROR: NewApp: %v\n", err)
		return
	}
	err = app.Run(csswg_resolutions_id)
	if err != nil {
		log.Printf("ERROR: app.Run: %v\n", err)
		return
	}
}
