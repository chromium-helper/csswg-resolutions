package main

import (
   "golang.org/x/oauth2"
  "github.com/google/go-github/v49/github"
  "fmt"
  "context"
  "time"
  "log"
  "regexp"
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

type CSSWGResolution struct {
  CommentID int64
  IssueNumber int

  Resolution string
  CommentURL string
}

// Parse the github resolutions
func parseResolutions(comments []*github.IssueComment) ([]*CSSWGResolution, error) {
  r, err := regexp.Compile("(?m)^[ `*]*RESOLVED: .*$")
  if err != nil {
    return nil, fmt.Errorf("regexp.Compile: %v\n", err)
  }

  for _, comment := range comments {
    rtext := r.FindAllString(*comment.Body, -1)
    fmt.Printf("%v\n", rtext)
  }
  return []*CSSWGResolution{}, nil
}

// Records the resolutions by creating issues if needed
func (app *App) recordResolutionsIfNeeded(resolutions []*CSSWGResolution) error {
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

  if len(resolutions) != 0 {
    app.ensureGithubRWClient()
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
