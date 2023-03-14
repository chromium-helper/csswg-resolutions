package triage_task_handler

import (
	"context"
	"fmt"
	"google.golang.org/api/idtoken"
	"log"
	"net/http"
  "golang.org/x/oauth2"
  "os"
  "regexp"
  "strings"
  "strconv"

	"github.com/chromium-helper/csswg-resolutions/monorail"
  "github.com/google/go-github/github"
	"github.com/chromium-helper/csswg-resolutions/fsresolutions"
  gcpfs "cloud.google.com/go/firestore"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

const (
  gcpProjectId = os.Getenv("GCP_PROJECT_ID")
  gcpGithubAPIKeySecret = os.Getenv("GCP_GITHUB_API_KEY_SECRET_NAME")
  gcpFsCollection = os.Getenv("GCP_FS_COLLECTION")
  githubLogin = os.Getenv("GITHUB_LOGIN")
  githubRepo = os.Getenv("GITHUB_REPO")
)

func fileMonorailIssue(ghissue *github.Issue, component string) (*monorail.Issue, error) {
  audience, err := monorail.GetAudience("prod")
  if err != nil {
    return nil, fmt.Errorf("GetAudience: %v\n", err)
  }

  ctx := context.Background()
  token_source, err := idtoken.NewTokenSource(ctx, audience)
  if err != nil {
    return nil, fmt.Errorf("NewTokenSource: %v\n", err)
  }

  service, err := monorail.NewIssuesService(ctx, "prod", token_source)
  if err != nil {
    return nil, fmt.Errorf("monorail.NewIssuesService: %v\n", err)
  }

  re := regexp.MustCompile(`\r?\n`)

  description := re.ReplaceAllString(ghissue.GetBody(), "\\n")
  description += `\n\n`
  description += fmt.Sprintf("If no action is needed, feel free to close this bug. Otherwise, please prioritize the work needed for the above resolutions.");
  description += `\n\n`
  description += fmt.Sprintf("Note that this issue has been triaged via https://github.com/chromium-helper/csswg-resolutions/issues/%d", ghissue.GetNumber())

  request := &monorail.CreateIssueRequest{
    Project: "chromium",
    Summary: ghissue.GetTitle(),
    Description: description,
    Components: []string{component},
  }
  issue, err := service.CreateIssue(request)
  if err != nil {
    return nil, fmt.Errorf("monorail.CreateIssue: %v\n", err)
  }
  return issue, nil
}

func commentAndClose(ghissue *github.Issue, crbug_id int) error {
  token, err := getGithubAPIToken()
  if err != nil {
    return fmt.Errorf("getGithubAPIToken: %v\n", err)
  }

  ctx := context.Background()

  token_source := oauth2.StaticTokenSource(
      &oauth2.Token{AccessToken: token})
  token_client := oauth2.NewClient(ctx, token_source)

  client := github.NewClient(token_client);

  // TODO: Figure out if I can get these from ghissue.
  owner := kResolutionsOwner
  repo := kResolutionsRepo

  // Add a comment
  comment_text := fmt.Sprintf("I have filed [crbug.com/%d](https://crbug.com/%d)\n\n", crbug_id, crbug_id)
  comment_text += "That is all that can be done here, closing issue."
  comment := &github.IssueComment{ Body: &comment_text }
  _, _, err = client.Issues.CreateComment(
    ctx, owner, repo, ghissue.GetNumber(), comment)
  if err != nil {
    return fmt.Errorf("Issues.CreateComment: %v\n", err)
  }

  // Close the issue.
  new_state := "closed"
  close_request := &github.IssueRequest{ State: &new_state }
  _, _, err = client.Issues.Edit(
      ctx, owner, repo, ghissue.GetNumber(), close_request)
  if err != nil {
    return fmt.Errorf("Issues.Edit: %v\n", err)
  }
  return nil
}

func processIssuesEvent(event *github.IssuesEvent) error {
  if event.GetAction() != "labeled" {
    return nil
  }

  if !strings.HasPrefix(event.GetLabel().GetName(), "crbug:") {
    return nil
  }

  if event.GetIssue().GetState() == "closed" {
    return nil
  }

  for _, label := range event.GetIssue().Labels {
    if label.GetName() == "meta" {
      return nil
    }
  }

  fsdata, err := loadFsResolutionData(event.GetIssue().GetNumber())
  if err != nil {
    return fmt.Errorf("loadFsResolutionData: %v\n", err)
  }

  if fsdata.CrbugId != 0 {
    // TODO: Figure out if we should do something other than error
    return fmt.Errorf("crbug already filed %d\n", fsdata.CrbugId)
  }

  component := strings.Split(event.GetLabel().GetName(), ":")[1]
  crbug, err := fileMonorailIssue(event.GetIssue(), component)
  if err != nil {
    return fmt.Errorf("fileMonorailIssue: %v\n", err)
  }

  commentErr := commentAndClose(event.GetIssue(), crbug.Id)

  fsdata.CrbugId = crbug.Id
  err = saveFsData(fsdata)

  if commentErr != nil {
    return fmt.Errorf("commentAndClose: %v\n", commentErr)
  }
  if err != nil {
    return fmt.Errorf("saveFsData: %v\n", err)
  }
  return nil
}







type App struct {
  FSClient *fsresolutions.Client
  GithubClient *github.Client
}

func NewApp() (*App, error) {
  fsclient, err := fsresolutions.NewClient(gcpProject, gcpFsCollection)
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
    Name:
      fmt.Sprintf("projects/%s/secrets/%s/versions/latest",
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
  github.NewClient(token_client), nil
}

func fileNameFromData(fsdata *fsresolutions.FSResolutionData) string {
  return fmt.Sprintf("%d", fsdata.CsswgDraftsId)
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
  defer app.GithubClient.Close()

  // TODO: implement.......

  return nil
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

