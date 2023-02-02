package main

import (
  "github.com/chromium-helper/csswg-resolutions/monorail"
  "golang.org/x/oauth2/google"
  "context"
)

const (
  kTarget = "prod"
)

func main() {
  ctx := context.Background()
  //audience, err := monorail.GetAudience(kTarget)
  //if err != nil {
  //  panic(err)
  //}

  token_source, err := google.DefaultTokenSource(ctx/*, audience*/)
  if err != nil {
    panic(err)
  }

  service, err := monorail.NewIssuesService(ctx, kTarget, token_source)
  if err != nil {
    panic(err)
  }

  request := &monorail.CreateIssueRequest{
    Project: "chromium",
    Summary: "vmpstr test bug #2",
    Description: `hello, this is another test bug!\nthanks!\n`,
    Components: []string{},
  }
  err = service.CreateIssue(request)
  if err != nil {
    panic(err)
  }
}
