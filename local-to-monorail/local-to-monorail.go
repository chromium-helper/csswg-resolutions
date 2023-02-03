// Package p contains an HTTP Cloud Function.
package foo

import (
	"context"
	"fmt"
	"github.com/chromium-helper/csswg-resolutions/monorail"
	"google.golang.org/api/idtoken"
	"log"
	"net/http"
  "github.com/google/go-github/github"
  "golang.org/x/oauth2"
  "os"
  "regexp"
  "strings"
  gcpfs "cloud.google.com/go/firestore"
  gcpsm "cloud.google.com/go/secretmanager/apiv1"
  gcpsmpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

const (
  gcpProject = "chromium-csswg-helper"
  gcpGithubAPIKeySecret = "github-api-key"
  gcpFirestoreCollection = "resolution-db"
  kResolutionsOwner = "chromium-helper"
  kResolutionsRepo = "csswg-resolutions"
)

type FSResolutionData struct {
  CrbugId int `firestore:"crbug-id"`
  CsswgDraftsId int `firestore:"csswg-drafts-id"`
  CsswgResolutionsId int `firestore:"csswg-resolutions-id"`
  ResolutionCommentIds []int64 `firestore:"resolution-comment-ids"`
}

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

func getGithubAPIToken() (string, error) {
  ctx := context.Background()
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
  comment_text := fmt.Sprintf("I have filed [crbug.com/%d](crbug.com/%d)\n\n", crbug_id, crbug_id)
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

func saveFsData(fsdata *FSResolutionData) error {
  ctx := context.Background()

  client, err := gcpfs.NewClient(ctx, gcpProject)
  if err != nil {
    return fmt.Errorf("gcpfs.NewClient: %v\n", err)
  }

  docname := fmt.Sprintf("%d", fsdata.CsswgDraftsId)
  _, err = client.Collection(gcpFirestoreCollection).Doc(docname).Update(
      ctx, []gcpfs.Update{{ Path: "crbug-id", Value: fsdata.CrbugId }})
  if err != nil {
    return fmt.Errorf("doc.Update: %v\n", err)
  }
  return nil
}

func loadFsResolutionData(number int) (*FSResolutionData, error) {
  ctx := context.Background()

  client, err := gcpfs.NewClient(ctx, gcpProject)
  if err != nil {
    return nil, fmt.Errorf("gcpfs.NewClient: %v\n", err)
  }

  query := client.Collection(gcpFirestoreCollection).Where(
      "csswg-resolutions-id", "==", number)
  iter := query.Documents(ctx)
  doc, err := iter.Next()
  if err != nil {
    // TODO: This could be iterator.Done, what should happen if
    // we don't know about this issue in the firestore?
    return nil, fmt.Errorf("iter.Next: %v\n", err)
  }

  var data FSResolutionData
  err = doc.DataTo(&data)
  if err != nil {
    return nil, fmt.Errorf("doc.DataTo: %v\n", err)
  }
  return &data, nil
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

func HelloWorld(w http.ResponseWriter, r *http.Request) {
  payload, err := github.ValidatePayload(r, []byte(os.Getenv("GITHUB_SECRET_KEY")))
  if err != nil { 
    log.Printf("ValidatePayload: ERROR: %v\n", err);
    return;
  }
  event, err := github.ParseWebHook(github.WebHookType(r), payload)
  if err != nil {
    log.Printf("ParseWebHook: ERROR: %v\n", err);
    return;
  }

  switch event := event.(type) {
    case *github.IssuesEvent:
      err = processIssuesEvent(event)
    default:
      log.Printf("not an issue event\n");
  }

  if err != nil {
    log.Printf("process event: ERROR: %v\n", err);
    return
  }
}

