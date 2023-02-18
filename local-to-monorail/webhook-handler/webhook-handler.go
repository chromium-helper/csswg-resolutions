package webhook_handler_cf

import (
  "net/http"
  "github.com/google/go-github/github"
  "time"
)

const (
  kTriageGracePeriod = 15 * time.Second
)

type FSResolutionData struct {
  Version int                  `firestore:"version,omitempty"`
  CrbugId int                  `firestore:"crbug-id,omitempty"`
  CsswgDraftsId int            `firestore:"csswg-drafts-id,omitempty"`
  CsswgResolutionsId int       `firestore:"csswg-resolutions-id,omitempty"`
  ResolutionCommentIds []int64 `firestore:"resolution-comment-ids,omitempty"`
  HasPendingTriageEvents bool  `firestore:"has-pending-triage-events,omitempty"`
}

// Figures out if this is likely an event that we care about.
// Note that this doesn't have to be 100% accurate. It should acts
// as a quick filter for things we definitely don't care about.
func ShouldCreateTaskForEvent(event *github.IssuesEvent) bool {
  // We only care about labeled and commented events.
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

// Processes an issue event:
// 1. Verify that we care about this issue
// 2. Find the firestore entry and update has_pending_triage_events
// 3. Create a task and schedule kTriageGracePeriod seconds in the future
func ProcessIssuesEvent(event *github.IssuesEvent) error {
  if !ShouldCreateTaskForEvent(event) {
    return nil
  }

  fsdata, err := LoadFsData(event.GetIssue().GetNumber())
  if err != nil {
    return fmt.Errorf("LoadFsData: %v", err)
  }

  // If the version doesn't match or there's already pending events,
  // then we shouldn't do anything.
  if fsdata.Version != kVersion || fsdata.HasPendingTriageEvents {
    return nil
  }

  fsdata.HasPendingTriageEvents = true
  err := UpdateFsPendingTriageEvents(fsdata)
  if err != nil {
    return fmt.Errorf("UpdateFsPendingTriageEvents: %v", err)
  }

  err := ScheduleTask(fsdata)
  if err != nil {
    return fmt.Errorf("ScheduleTask: %v", err)
  }
  return nil
}

// Entry point for the github webhook.
func HandleGithubWebhook(w http.ResponseWriter, r *http.Request) {
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
      err = ProcessIssuesEvent(event)
    default:
      log.Printf("not an issue event\n");
  }

  if err != nil {
    log.Printf("process event: ERROR: %v\n", err);
    return
  }
}
