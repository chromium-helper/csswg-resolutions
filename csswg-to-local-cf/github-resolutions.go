// Package p contains a Pub/Sub Cloud Function.
package p

import (
  "golang.org/x/oauth2"
  "github.com/google/go-github/github"
  "fmt"
  "context"
  "time"
  "log"
  "regexp"
  "strings"
  "strconv"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
  "github.com/chromium-helper/csswg-resolutions/fsresolutions"
)

const (
  gcpProject = "chromium-csswg-helper"
  gcpGithubAPIKeySecret = "github-api-key"
  gcpFirestoreCollection = "resolution-db"

  csswgOwner = "w3c"
  csswgRepo = "csswg-drafts"

  resOwner = "chromium-helper"
  resRepo = "csswg-resolutions"
)

type App struct {
  gh_client_ro *github.Client
  gh_client_rw *github.Client
  StartTime time.Time
  FSClient *fsresolutions.Client
}

type CSSWGResolution struct {
  CommentID int64
  IssueNumber int

  Resolutions []string
  CommentURL string
}

// Creates a new app to use
func NewApp() *App {
  fsclient, err := fsresolutions.NewClient(gcpProject, gcpFirestoreCollection)
  if err != nil {
    panic(err)
  }
  return &App{
    gh_client_ro: github.NewClient(nil),
    gh_client_rw: nil,
    StartTime: time.Now(),
    FSClient: fsclient,
  }
}

// Retrieves the github api token from gcp secret manager. Don't use unless you
// need write access to github.
func (app *App) getGithubAPIToken() (string, error) {
  client, err := gcpsm.NewClient(context.Background())
  if err != nil {
    return "", fmt.Errorf("gcpsm.NewClient: %v", err)
  }
  defer client.Close()

  req := &gcpsmpb.AccessSecretVersionRequest{
    Name:
      fmt.Sprintf("projects/%s/secrets/%s/versions/latest",
        gcpProject,
        gcpGithubAPIKeySecret,
      ),
  }

  secret, err := client.AccessSecretVersion(context.Background(), req)
  if err != nil {
    return "", fmt.Errorf("gcpsm.AccessSecretVersion: %v\n", err)
  }
  return string(secret.Payload.GetData()), nil
}

// Ensures there is a github read-write client for creating issues, etc
func (app *App) ensureGithubRWClient() error {
  if app.gh_client_rw != nil {
    return nil
  }

  token, err := app.getGithubAPIToken()
  if err != nil {
    return fmt.Errorf("getGithubAPIToken: %v\n", err)
  }

  token_source := oauth2.StaticTokenSource(
      &oauth2.Token{AccessToken: token})
  token_client := oauth2.NewClient(context.Background(), token_source)

  app.gh_client_rw = github.NewClient(token_client);
  return nil
}

// Get the "best" github client (RW if available, RO otherwise)
func (app *App) github_client() *github.Client {
  if app.gh_client_rw != nil {
    return app.gh_client_rw
  }
  return app.gh_client_ro
}

// Get all the issue comments for csswg since the given time.
func (app *App) getIssueComments(since time.Time) (
    []*github.IssueComment, error) {
  opts := &github.IssueListCommentsOptions{
    Sort: "created",
    Since: since,
    ListOptions: github.ListOptions{ PerPage: 100 },
  }

  var results []*github.IssueComment
  for {
    comments, resp, err := app.github_client().Issues.ListComments(
      context.Background(),
      csswgOwner,
      csswgRepo,
      0,
      opts,
    )
    if err != nil {
      return nil, err
    }
    for _, comment := range comments {
      if comment.GetCreatedAt().Before(since) {
        continue
      }
      results = append(results, comment)
    }

    if resp.NextPage == 0 {
      break;
    }
    opts.Page = resp.NextPage
  }
  return results, nil
}

// Parse the github resolutions
func parseResolutions(comments []*github.IssueComment) (
    []*CSSWGResolution, error) {
  r, err := regexp.Compile("(?m)^[ `*]*RESOLVED: .*$")
  if err != nil {
    return nil, fmt.Errorf("regexp.Compile: %v\n", err)
  }

  var results []*CSSWGResolution
  for _, comment := range comments {
    matches := r.FindAllString(*comment.Body, -1)
    if len(matches) == 0 {
      continue
    }

    url_parts := strings.Split(*comment.IssueURL, "/")
    issue_number, err := strconv.Atoi(url_parts[len(url_parts)-1])
    if err != nil {
      return nil, fmt.Errorf("atoi for url %s: %v\n", comment.IssueURL, err)
    }

    resolution := &CSSWGResolution{
      CommentID: *comment.ID,
      IssueNumber: issue_number,
      CommentURL: *comment.HTMLURL,
    }
    resolution.Resolutions = append(resolution.Resolutions, matches...)
    results = append(results, resolution)
  }
  return results, nil
}

func contains(needle int64, haystack []int64) bool {
  for _, candidate := range haystack {
    if needle == candidate {
      return true
    }
  }
  return false
}

// Records the resolutions by creating issues if needed
func (app *App) recordResolutionsIfNeeded(
    resolutions []*CSSWGResolution) error {
  for _, resolution := range resolutions {
    // See if we have this issue in the firestore.
    docname := fmt.Sprintf("%d", resolution.IssueNumber)
    data, err := app.FSClient.LoadDataByDocName(docname)
    if err != nil {
       return fmt.Errorf("LoadDataByDocName: %v", err)
    }

    if data == nil {
      if err = app.createNewIssue(resolution, docname); err != nil {
        return fmt.Errorf("app.createNewIssue: %v", err)
      }
      continue
    }

    // We already recorded this.
    // TODO: The text of the resolution could've changed, but
    // we're not storing the text to compare though. If needed,
    // we should change this.
    if contains(resolution.CommentID, data.ResolutionCommentIds) {
      continue
    }

    err = app.addResolutionComment(resolution, docname, data)
    if err != nil {
      return fmt.Errorf("app.addResolutionComment: %v", err)
    }
  }
  return nil
}

func createIssueText(resolutions []string, commentURL string) string {
  body := fmt.Sprintf("CSSWG added the following resolution(s):\n\n")
  for _, resolution := range resolutions {
    body += fmt.Sprintf("> %s\n", resolution)
  }
  body += fmt.Sprintf("\nin %s", commentURL)
  return body
}

func (app *App) createNewIssue(
    resolution *CSSWGResolution, docname string) error {
  err := app.ensureGithubRWClient()
  if err != nil {
    return fmt.Errorf("ensure rw client: %v\n", err)
  }
  csswgissue, _, err := app.github_client().Issues.Get(
      context.Background(), csswgOwner, csswgRepo, resolution.IssueNumber)
  if err != nil {
    return fmt.Errorf("gh.Issues.Get: %v\n", err)
  }

  title := *csswgissue.Title
  body := createIssueText(resolution.Resolutions, resolution.CommentURL)
  var labels []string
  for _, rlabel := range csswgissue.Labels {
    if strings.HasPrefix(rlabel.GetName(), "css-") {
      labels = append(labels, rlabel.GetName())
    }
  }

  request := &github.IssueRequest{
    Title: &title,
    Body: &body,
  }
  if len(labels) != 0 {
    request.Labels = &labels
  }

  resissue, _, err := app.github_client().Issues.Create(
      context.Background(), resOwner, resRepo, request)
  if err != nil {
    return fmt.Errorf("github.CreateIssue: %v", err)
  }
  log.Printf("Created new issue #%d: %s\n", resissue.GetNumber(), title)

  fsdata := &fsresolutions.FSResolutionData{
    CsswgDraftsId: csswgissue.GetNumber(),
    CsswgResolutionsId: resissue.GetNumber(),
    ResolutionCommentIds: []int64{resolution.CommentID},
  }
  if err = app.FSClient.SetData( docname, fsdata); err != nil {
    return fmt.Errorf("SetData: %v", err)
  }
  return nil
}

func (app *App) addResolutionComment(
    resolution *CSSWGResolution,
    docname string,
    data *fsresolutions.FSResolutionData) error {
  err := app.ensureGithubRWClient()
  if err != nil {
    return fmt.Errorf("ensure rw client: %v\n", err)
  }
  body := createIssueText(resolution.Resolutions, resolution.CommentURL)
  comment := &github.IssueComment{ Body: &body }
  _, _, err = app.github_client().Issues.CreateComment(
      context.Background(), resOwner, resRepo, data.CsswgResolutionsId, comment)
  if err != nil {
    return fmt.Errorf("github.CreateComment: %v\n", err)
  }
  log.Printf("Added comment to issue #%d\n", data.CsswgResolutionsId)

  data.ResolutionCommentIds =
    append(data.ResolutionCommentIds, resolution.CommentID)
  if err = app.FSClient.UpdateDataSetResolutionCommentIds(docname, data);
     err != nil {
    return fmt.Errorf("UpdateDataSetResolutionCommentIds: %v", err)
  }
  return nil
}

// Main run function for the app.
func (app *App) run() error {
  last_run_time, err := app.FSClient.LoadLastRunTime()
  if err != nil {
    log.Printf("loadLastRunTime: %v\n", err)
    return err
  }
  log.Printf("last run time %v", last_run_time.String())

  comments, err := app.getIssueComments(last_run_time)
  if err != nil {
    log.Printf("getIssueComments: %v\n", err)
    return err
  }

  resolutions, err := parseResolutions(comments)
  if err != nil {
    log.Printf("parseResolutions: %v\n", err)
    return err
  }
  log.Printf("resolutions %v\n", resolutions)

  err = app.recordResolutionsIfNeeded(resolutions)
  if err != nil {
    log.Printf("recordResolutionsIfNeeded: %v\n", err)
    return err
  }

  if err = app.FSClient.UpdateLastRunTime(app.StartTime); err != nil {
    log.Printf("UpdateLastRunTime: %v\n", err)
    return err
  }
  return nil
}

type PubSubMessage struct {
	Data []byte `json:"data"`
}

// Entry point to the timer CF.
func ParseCsswgResolutions(ctx context.Context, m PubSubMessage) error {
	return NewApp().run()
}

