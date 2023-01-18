package main

import (
  _ "golang.org/x/oauth2"
  "github.com/google/go-github/v49/github"
  "fmt"
  "context"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

const (
  gcpProject = "chromium-csswg-helper"
  gcpGithubAPIKeySecret = "github-api-key"
)

func getGithubAPIToken(ctx context.Context) (string, error) {
  client, err := gcpsm.NewClient(ctx)
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

  secret, err := client.AccessSecretVersion(ctx, req)
  if err != nil {
    return "", fmt.Errorf("gcpsm.AccessSecretVersion: %v\n", err)
  }
  return string(secret.Payload.GetData()), nil
}

func main() {
  ctx := context.Background()
  //token, err := getGithubAPIToken(ctx)
  //if err != nil {
  //  panic(err)
  //}

  //ts := oauth2.StaticTokenSource(
  //  &oauth2.Token{AccessToken: token},
  //)
  //tc := oauth2.NewClient(ctx, ts)

  //client := github.NewClient(tc);
  read_client := github.NewClient(nil);

  issues, _, err := read_client.Issues.ListByRepo(ctx, "w3c", "csswg-drafts", nil)
  if err != nil {
    panic(err)
  }

  for _, issue := range issues {
    fmt.Printf("%d: %s\n", *issue.Number, *issue.Title)
  }
}

