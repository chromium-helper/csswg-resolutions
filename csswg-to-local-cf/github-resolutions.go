package main

import (
  "golang.org/x/oauth2"
  "github.com/google/go-github/v49/github"
  "fmt"
  "context"
  "time"
  "log"
  "regexp"
  "strings"
  "strconv"
  "google.golang.org/grpc/status"
  "google.golang.org/grpc/codes"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
  gcpfs "cloud.google.com/go/firestore"
)

const (
  gcpProject = "chromium-csswg-helper"
  gcpGithubAPIKeySecret = "github-api-key"
  gcpFirestoreCollection = "resolution-db"

  csswgOwner = "w3c"
  csswgRepo = "csswg-drafts"
)

type App struct {
  gh_client_ro *github.Client
  gh_client_rw *github.Client
  Ctx context.Context
  StartTime time.Time
}

type CSSWGResolution struct {
  CommentID int64
  IssueNumber int

  Resolutions []string
  CommentURL string
}

// Creates a new app to use
func NewApp(ctx context.Context) *App {
  return &App{
    gh_client_ro: github.NewClient(nil),
    gh_client_rw: nil,
    Ctx: ctx,
    StartTime: time.Now(),
  }
}

// Retrieves the github api token from gcp secret manager. Don't use unless you
// need write access to github.
func (app *App) getGithubAPIToken() (string, error) {
  client, err := gcpsm.NewClient(app.Ctx)
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

  secret, err := client.AccessSecretVersion(app.Ctx, req)
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
  token_client := oauth2.NewClient(app.Ctx, token_source)

  app.gh_client_rw = github.NewClient(token_client);
  return nil
}

// Loads the last time this CF ran from gcp firestore
func (app *App) loadLastRunTime() (time.Time, error) {
  client, err := gcpfs.NewClient(app.Ctx, gcpProject)
  if err != nil {
    return time.Time{}, fmt.Errorf("gcpfs.NewClient: %v\n", err)
  }
  defer client.Close()

  docsnap, err := client.Collection(gcpFirestoreCollection).Doc("last_run").Get(app.Ctx)
  if err != nil {
    return time.Time{}, fmt.Errorf("client.Collection.Doc.Get: %v\n", err)
  }

  type Data struct {
    Time time.Time `firestore:"time"`
  }
  var data Data
  err = docsnap.DataTo(&data)
  if err != nil {
    return time.Time{}, fmt.Errorf("docsnap.DataTo: %v\n", err)
  }
  return data.Time, nil
}

// Get the "best" github client (RW if available, RO otherwise)
func (app *App) github_client() *github.Client {
  if app.gh_client_rw != nil {
    return app.gh_client_rw
  }
  return app.gh_client_ro
}

// Get all the issue comments for csswg since the given time.
func (app *App) getIssueComments(since time.Time) ([]*github.IssueComment, error) {
  sort := "created"
  opts := &github.IssueListCommentsOptions{
    Sort: &sort,
    Since: &since,
    ListOptions: github.ListOptions{ PerPage: 100 },
  }

  var results []*github.IssueComment
  for {
    comments, resp, err := app.github_client().Issues.ListComments(
      app.Ctx,
      csswgOwner,
      csswgRepo,
      0,
      opts,
    )
    if err != nil {
      return nil, err
    }
    results = append(results, comments...)
    if resp.NextPage == 0 {
      break;
    }
    opts.Page = resp.NextPage
  }
  return results, nil
}

// Parse the github resolutions
func parseResolutions(comments []*github.IssueComment) ([]*CSSWGResolution, error) {
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

type FSResolutionData struct {
  CrbugId int `firestore:"crbug-id"`
  CsswgDraftsId int `firestore:"csswg-drafts-id"`
  CsswgResolutionsId int `firestore:"csswg-resolutions-id"`
  ResolutionCommentIds []int64 `firestore:"resolution-comment-ids"`
}

// Records the resolutions by creating issues if needed
func (app *App) recordResolutionsIfNeeded(resolutions []*CSSWGResolution) error {
  fsclient, err := gcpfs.NewClient(app.Ctx, gcpProject)
  if err != nil {
    return fmt.Errorf("gcpfs.NewClient: %v\n", err)
  }
  defer fsclient.Close()

  for _, resolution := range resolutions {
    // See if we have this issue in the firestore.
    docname := fmt.Sprintf("%d", resolution.IssueNumber)
    docsnap, err := fsclient.Collection(gcpFirestoreCollection).Doc(docname).Get(app.Ctx)
    if err != nil && status.Code(err) == codes.NotFound {
      err = app.createNewIssue(resolution, fsclient, docname)
      if err != nil {
        return fmt.Errorf("app.createNewIssue: %v\n", err)
      }
      continue
    } else if err != nil {
      return fmt.Errorf("fsclient...: %v\n", err)
    }

    var data FSResolutionData
    err = docsnap.DataTo(&data)
    if err != nil {
      return fmt.Errorf("docsnap.DataTo: %v\n", err)
    }

    // We already recorded this. 
    // TODO: The text of the resolution could've changed, but
    // we're not storing the text to compare though. If needed,
    // we should change this.
    if contains(resolution.CommentID, data.ResolutionCommentIds) {
      continue
    }

    err = app.addResolutionComment(resolution, fsclient, docname, &data)
    if err != nil {
      return fmt.Errorf("app.addResolutionComment: %v\n", err)
    }
  }
  return nil
}

func (app *App) createNewIssue(resolution *CSSWGResolution, fsclient *gcpfs.Client, docname string) error {
  //app.ensureGithubRWClient()
  // TODO: Implement
  return nil
}

func (app *App) addResolutionComment(resolution *CSSWGResolution, fsclient *gcpfs.Client, docname string, data *FSResolutionData) error {
  //app.ensureGithubRWClient()
  // TODO: Implement
  return nil
}

func (app *App) updateLastRunTime(t time.Time) error {
  // TODO: Implement
  return nil
}

// Main run function for the app.
func (app *App) run() {
  last_run_time, err := app.loadLastRunTime()
  if err != nil {
    log.Printf("loadLastRunTime: %v\n", err)
    return
  }

  comments, err := app.getIssueComments(last_run_time)
  if err != nil {
    log.Printf("getIssueComments: %v\n", err)
    return
  }

  resolutions, err := parseResolutions(comments)
  if err != nil {
    log.Printf("parseResolutions: %v\n", err)
    return
  }

  err = app.recordResolutionsIfNeeded(resolutions)
  if err != nil {
    log.Printf("recordResolutionsIfNeeded: %v\n", err)
    return
  }

  err = app.updateLastRunTime(app.StartTime)
  if err != nil {
    log.Printf("updateLastRunTime: %v\n", err)
    return
  }
}

func main() {
  NewApp(context.Background()).run()
}
