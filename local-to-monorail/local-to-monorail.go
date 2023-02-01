package main

import (
  "github.com/chromium-helper/csswg-resolutions/monorail"
  "google.golang.org/api/idtoken"
  "context"
)

func main() {
  opts := idtoken.WithCredentialsFile("XXX")
  service, err := monorail.NewIssuesService(context.Background(), "prod", opts)
  if err != nil {
    panic(err)
  }

  request := &monorail.CreateIssueRequest{
    Project: "chromium",
    Summary: "vmpstr test bug",
    Description: `hello, this is a test bug!\nthanks!\n`,
    Components: []string{"Blink>ViewTransitions"},
  }
  err = service.CreateIssue(request)
  if err != nil {
    panic(err)
  }
}
