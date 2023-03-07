package webhook_handler_cf

import (
  "net/http"
  "fmt"
  "log"
  "os"
  "strconv"
  "context"
  "time"

  "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
  "github.com/chromium-helper/csswg-resolutions/fsresolutions"
  "github.com/google/go-github/github"
  "google.golang.org/protobuf/types/known/timestamppb"
  cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
)

// Environment that needs to be configured.
// GITHUB_SECRET_KEY: the secret webhook key to validate payload
// GCP_PROJECT_ID: the project where this is running
// GCP_QUEUE_LOCATION: the data centre location of the task queue
// GCP_QUEUE_ID: the name of the task queue
// GCP_FS_COLLECTION: the name of the firestore collection for fsresolutions
// GCP_TASK_HANDLER_URL: the url of the cloud function which handles the tasks
// TRIAGE_GRACE_PERIOD_SECONDS: the number of seconds to delay running the task,
//    allowing for more triager actions
var (
  githubSecretKey = os.Getenv("GITHUB_SECRET_KEY")

  gcpProjectId = os.Getenv("GCP_PROJECT_ID")
  gcpQueueLocation = os.Getenv("GCP_QUEUE_LOCATION")
  gcpQueueId = os.Getenv("GCP_QUEUE_ID")
  gcpFsCollection = os.Getenv("GCP_FS_COLLECTION")
  gcpTaskHandlerUrl = os.Getenv("GCP_TASK_HANDLER_URL")

  kTriageGracePeriod =
      time.Duration(mustInt(os.Getenv("TRIAGE_GRACE_PERIOD_SECONDS"))) * time.Second
)

func mustInt(s string) int {
  n, err := strconv.Atoi(s)
  if err != nil {
    panic(err)
  }
  return n
}

// Figures out if this is likely an event that we care about.
// Note that this doesn't have to be 100% accurate. It should acts
// as a quick filter for things we definitely don't care about.
func ShouldCreateTaskForEvent(event *github.IssuesEvent) bool {
  // We only care about labeled and commented events.
  // For the comments, we'd want to also filter by collaborators, but that
  // requires authentication. We'll have to do this in the task anyway.
  if event.GetAction() != "labeled" && event.GetAction() != "commented" {
    return false
  }

  // We also never handle "meta" tagged bugs
  for _, label := range event.GetIssue().Labels {
    if label.GetName() == "meta" {
      return false
    }
  }

  return true
}

func ScheduleTask(fsdata *fsresolutions.FSResolutionData) error {
  ctx := context.Background()
  client, err := cloudtasks.NewClient(ctx)
  if err != nil {
    return fmt.Errorf("cloudtasks.NewClient: %v", err)
  }

  http_request := &cloudtaskspb.HttpRequest{
    Url: gcpTaskHandlerUrl,
    Headers: map[string]string{
        "Content-Type": "application/x-www-form-urlencoded",
    },
    Body: []byte(fmt.Sprintf("CsswgResolutionsId=%d", fsdata.CsswgResolutionsId)),
  }

  task := &cloudtaskspb.Task{
    MessageType: &cloudtaskspb.Task_HttpRequest{ HttpRequest: http_request },
    ScheduleTime: timestamppb.New(time.Now().Add(kTriageGracePeriod)),
  }

  request := &cloudtaskspb.CreateTaskRequest{
    Parent: fmt.Sprintf("projects/%s/locations/%s/queues/%s",
                gcpProjectId, gcpQueueLocation, gcpQueueId),
    Task: task,
  }

  _, err = client.CreateTask(ctx, request)
  if err != nil {
    return fmt.Errorf("CreateTask: %v", err)
  }
  return nil
}

// Processes an issue event:
// 1. Verify that we care about this issue
// 2. Find the firestore entry and update has_pending_triage_events
// 3. Create a task and schedule kTriageGracePeriod seconds in the future
func ProcessIssuesEvent(event *github.IssuesEvent) error {
  if !ShouldCreateTaskForEvent(event) {
    return nil
  }

  fsclient, err := fsresolutions.NewClient(gcpProjectId, gcpFsCollection)
  if err != nil {
    return fmt.Errorf("fsresolutions.NewClient: %v", err)
  }
  defer fsclient.Close()

  fsdata, err := fsclient.LoadDataByCsswgResolutionsId(event.GetIssue().GetNumber())
  if err != nil {
    return fmt.Errorf("LoadDataByCsswgResolutionsId: %v", err)
  }

  // If the version doesn't match or there's already pending events,
  // then we shouldn't do anything.
  if fsdata.Version != fsresolutions.Version || fsdata.HasPendingTriageEvents {
    return nil
  }

  fsdata.HasPendingTriageEvents = true
  err = fsclient.UpdateDataSetHasPendingTriageEvents(
      fmt.Sprintf("%d", fsdata.CsswgDraftsId), fsdata)
  if err != nil {
    return fmt.Errorf("UpdateDataSetHasPendingTriageEvents: %v", err)
  }

  err = ScheduleTask(fsdata)
  if err != nil {
    return fmt.Errorf("ScheduleTask: %v", err)
  }
  return nil
}

// Entry point for the github webhook.
func HandleGithubWebhook(w http.ResponseWriter, r *http.Request) {
  payload, err := github.ValidatePayload(r, []byte(githubSecretKey))

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
      err = ProcessIssuesEvent(event)
    default:
      log.Printf("not an issue event\n");
  }

  if err != nil {
    log.Printf("process event: ERROR: %v\n", err);
    return
  }
}
