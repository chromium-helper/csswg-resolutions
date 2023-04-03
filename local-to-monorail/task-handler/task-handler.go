package triage_task_handler

import (
	"context"
	"fmt"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"log"
	"net/http"
	"os"
	"regexp"
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
	if directive.Comment != "" {
		description += fmt.Sprintf("%s left an additional comment:\n%s\n\n", directive.Commenter, directive.Comment)
	}
	description += fmt.Sprintf("This issue has been triaged via https://github.com/chromium-helper/csswg-resolutions/issues/%d\n", ghissue.GetNumber())

	var issue *monorail.Issue
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
		issue, err = service.CreateIssue(request)
		if err != nil {
			return nil, fmt.Errorf("monorail.CreateIssue: %v", err)
		}
	} else {
		request := &monorail.ModifyIssueRequest{
			Project:		"chromium",
			Crbug:			directive.Crbug,
			Comment:		description,
			// TODO(vmpstr): Once we get permission, add owners/cc/components
		}
		err = service.ModifyIssue(request)
		if err != nil {
			return nil, fmt.Errorf("monorail.ModifyIssue: %v", err)
		}
		issue = &monorail.Issue{ Id: directive.Crbug }
	}
	return issue, nil
}

func (app *App) CommentAndClose(action string, ghissue *github.Issue, crbug_id int) error {
	ctx := context.Background()

	// Add a comment
	comment_text := fmt.Sprintf("I have %s [crbug.com/%d](https://crbug.com/%d)\n\n", action, crbug_id, crbug_id)
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

func ParseComponents(issue *github.Issue) ([]string, bool) {
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

func (app *App) ParseDirective(issue *github.Issue) (*Directive, bool, error) {
	var directive Directive
	var skip bool
	directive.Components, skip = ParseComponents(issue)
	if skip {
		return nil, true, nil
	}

	ctx := context.Background()
	collaborators, _, err := app.GithubClient.Repositories.ListCollaborators(ctx, githubLogin, githubRepo, nil)
	if err != nil {
		return nil, false, fmt.Errorf("ListCollaborators: %v")
	}

	collaborator_set := make(map[string]bool)
	for _, collaborator := range collaborators {
		collaborator_set[collaborator.GetLogin()] = true
	}

	get_user := func(input string) string {
		user := strings.Trim(input, " ")
		if !strings.Contains(user, "@") {
			user += "@chromium.org"
		}
		return user
	}

	comments, _, err := app.GithubClient.Issues.ListComments(ctx, githubLogin, githubRepo, issue.GetNumber(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("ListComments: %v")
	}

	for _, comment := range comments {
		if !collaborator_set[comment.GetUser().GetLogin()] {
			continue
		}

	  lower_body := strings.ToLower(comment.GetBody())
		lines := strings.Split(lower_body, "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "crbug:") || strings.HasPrefix(line, "bug:") {
				re := regexp.MustCompile(`[0-9]{5,}`)
				directive.Crbug, err = strconv.Atoi(re.FindString(line))
				if err != nil {
					fmt.Printf("WARNING: Could not parse crbug from '%s'\n", line)
					directive.Crbug = 0
				}
			} else if strings.HasPrefix(line, "owner:") {
				directive.Owner = get_user(line[len("owner:"):])
			} else if strings.HasPrefix(line, "cc:") {
				parts := strings.Split(line[len("cc:"):], ",")
				for _, part := range parts {
					directive.CcList = append(directive.CcList, get_user(strings.Trim(part, " ")))
				}
			} else if strings.HasPrefix(line, "comment:") {
				directive.Comment = strings.Trim(line[len("comment:"):], " ")
				directive.Commenter = comment.GetUser().GetLogin();
			}
		}
	}
	return &directive, false, nil
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

	directive, skip, err := app.ParseDirective(issue)
	if err != nil {
		return fmt.Errorf("ParseDirectives: %v")
	}

	// We need a component or a crbug
	if skip || (len(directive.Components) == 0 && directive.Crbug == 0) {
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
		action = "updated"
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
