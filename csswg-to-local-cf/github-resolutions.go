package main

import (
  "golang.org/x/oauth2"
  "github.com/google/go-github/v49/github"
  "fmt"
  "context"
)

func main() {
  ctx := context.Background()
  ts := oauth2.StaticTokenSource(
    &oauth2.Token{AccessToken: "..."},
  )
  tc := oauth2.NewClient(ctx, ts)

  client := github.NewClient(tc);

  issues, _, err := client.Issues.ListByRepo(ctx, "w3c", "csswg-drafts", nil)
  if err != nil {
    panic(err)
  }

  for _, issue := range issues {
    fmt.Printf("%d: %s\n", *issue.Number, *issue.Title)
  }

}

