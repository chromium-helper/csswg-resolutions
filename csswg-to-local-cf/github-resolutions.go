package main

import (
   "golang.org/x/oauth2"
  "github.com/google/go-github/v49/github"
  "fmt"
  "context"
  "time"
  "log"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
  gcpfs "cloud.google.com/go/firestore"
)

const (
  gcpProject = "chromium-csswg-helper"
  gcpGithubAPIKeySecret = "github-api-key"
  gcpFirestoreCollection = "resolution-db"
)

type App struct {
  gh_client_ro *github.Client
  gh_client_rw *github.Client
  ctx context.Context
  start_time time.Time
}

// Creates a new app to use
func NewApp(ctx context.Context) *App {
  return &App{
    gh_client_ro: github.NewClient(nil),
    gh_client_rw: nil,
    ctx: ctx,
    start_time: time.Now(),
  }
}

// Retrieves the github api token from gcp secret manager. Don't use unless you
// need write access to github.
func (app *App) getGithubAPIToken() (string, error) {
  client, err := gcpsm.NewClient(app.ctx)
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

  secret, err := client.AccessSecretVersion(app.ctx, req)
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
  token_client := oauth2.NewClient(app.ctx, token_source)

  app.gh_client_rw = github.NewClient(token_client);
  return nil
}

// Loads the last time this CF ran from gcp firestore
func (app *App) loadLastRunTime() (time.Time, error) {
  client, err := gcpfs.NewClient(app.ctx, gcpProject)
  if err != nil {
    return time.Time{}, fmt.Errorf("gcpfs.NewClient: %v\n", err)
  }
  defer client.Close()

  docsnap, err := client.Collection(gcpFirestoreCollection).Doc("last_run").Get(app.ctx)
  if err != nil {
    return time.Time{}, fmt.Errorf("client.Collection.Doc.Get: %v\n", err)
  }

  t, ok := docsnap.Data()["time"].(time.Time)
  if !ok {
    return time.Time{}, fmt.Errorf("docsnap.DataTo: not ok\n")
  }
  return t, nil
}

// Main run function for the app.
func (app *App) run() {
  last_run_time, err := app.loadLastRunTime()
  if err != nil {
    log.Printf("loadLastRunTime: %v\n", err)
    return
  };

  // TODO: This isn't implemented!
  // Some random code
  //  issues, _, err := app..Issues.ListByRepo(ctx, "w3c", "csswg-drafts", nil)
  //  if err != nil {
  //    panic(err)
  //  }
  //
  //  for _, issue := range issues {
  //    fmt.Printf("%d: %s\n", *issue.Number, *issue.Title)
  //  }
  //}
  //
  fmt.Printf("last run time %s\n", last_run_time.String())
}

func main() {
  NewApp(context.Background()).run()
}
