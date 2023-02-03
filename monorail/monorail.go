package monorail

import (
  "bytes"
  "context"
  "strings"
  "fmt"
  "net/http"
  "io/ioutil"
  "time"
  "golang.org/x/oauth2"
  "encoding/json"
)

type IssuesService struct {
  Token *oauth2.Token
  HttpClient *http.Client
  ApiBase string
}


type monorailIssue struct {
	Name  string `json:"name"`
	State struct {
		Status string `json:"status"`
	} `json:"status"`
	FieldValues []struct {
		Field string `json:"field"`
		Value string `json:"value"`
	} `json:"fieldValues"`
	Owner struct {
		User string `json:"user"`
	} `json:"owner"`
	CreatedTime  time.Time `json:"createTime"`
	ModifiedTime time.Time `json:"modifyTime"`
	ClosedTime   time.Time `json:"closeTime"`
	Title string `json:"summary"`
}

type Issue struct {
  Id int
  // TODO: Populate more stuff I guess?
}

func contains(needle string, haystack []string) bool {
  for _, candidate := range haystack {
    if needle == candidate {
      return true
    }
  }
  return false
}

func createHttpClient(token_source oauth2.TokenSource) (*http.Client, error) {
  transport := &oauth2.Transport{
    Source: token_source,
    Base: http.DefaultTransport,
  }
  return &http.Client{
    Transport: transport,
    Timeout: 1 * time.Minute,
  }, nil
}

func GetAudience(target string) (string, error) {
  if !contains(target, []string{"prod", "dev", "staging"}) {
    return "", fmt.Errorf("target must be one of prod, dev, staging\n")
  }
  return fmt.Sprintf("https://monorail-%s.appspot.com", target), nil
}

func NewIssuesService(ctx context.Context, target string, token_source oauth2.TokenSource) (
    *IssuesService, error) {
  if !contains(target, []string{"prod", "dev", "staging"}) {
    return nil, fmt.Errorf("target must be one of prod, dev, staging\n")
  }

  api_base := fmt.Sprintf("https://api-dot-monorail-%s.appspot.com/prpc", target)

  token, err := token_source.Token()
  if err != nil {
    return nil, fmt.Errorf("token_source.Token: %v\n", err)
  }

  http_client, err := createHttpClient(token_source)
  if err != nil {
    return nil, fmt.Errorf("createHttpClient: %v\n", err)
  }

	return &IssuesService{
    Token: token,
    HttpClient: http_client,
    ApiBase: api_base,
	}, nil
}

func (s *IssuesService) invokeApi(payload []byte, service string, method string) ([]byte, error) {
  url := fmt.Sprintf("%s/monorail.v3.%s/%s", s.ApiBase, service, method);

  request, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
  if err != nil {
    return nil, fmt.Errorf("http.NewRequest: %v\n", err)
  }

  request.Header.Add("Authorization", fmt.Sprintf("Bearer %s\n", s.Token.AccessToken))
  request.Header.Add("Content-Type", "application/json")
  request.Header.Add("Accept", "application/json")

  response, err := s.HttpClient.Do(request)
  if err != nil {
    return nil, fmt.Errorf("http.Client.Do: %v\n", err)
  }
  defer response.Body.Close()

  result, err := ioutil.ReadAll(response.Body)
  if err != nil {
    return nil, fmt.Errorf("ioutil.ReadAll: %v\n", err)
  }

  if response.StatusCode != 200 {
    return nil, fmt.Errorf("http response %d (%s)\nbody: %s", response.StatusCode, http.StatusText(response.StatusCode), string(result))
  }

  return result[4:], nil
}

type CreateIssueRequest struct {
  // "chromium"
  Project string

  Summary string
  Description string

  Components []string
}

func (s *IssuesService) CreateIssue(request *CreateIssueRequest) (*Issue, error) {
  var components []string
  for _, component := range request.Components {
    components = append(components, fmt.Sprintf(`{ "component": "projects/%s/componentDefs/%s" }`, request.Project, component))
  }
  // Field values ids are here:
  // https://bugs.chromium.org/p/chromium/adminLabels
  // 10 - Type
  // 11 - Pri
  typeField := fmt.Sprintf("projects/%s/fieldDefs/10", request.Project)
  priField := fmt.Sprintf("projects/%s/fieldDefs/11", request.Project)
  json := fmt.Sprintf(`{
    "parent": "projects/%s",
    "issue": {
      "status": { "status": "Untriaged" },
      "summary": "%s",
      "components": [%s],
      "field_values": [
        { "field": "%s", "value": "2" },
        { "field": "%s", "value": "Task" }
      ]
    },
    "description": "%s"
  }`,
  request.Project,
  request.Summary,
  strings.Join(components, ","),
  priField,
  typeField,
  request.Description)
  
  result, err := s.invokeApi([]byte(json), "Issues", "MakeIssue")
  if err != nil {
    return nil, fmt.Errorf("invokeApi: %v\n", err)
  }

  var monorail_issue *monorailIssue
  if err := json.Unmarshal(result, &monorail_issue); err != nil {
		return nil, fmt.Errorf("Unmarshal: %v\n", err)
	}

  name_parts := strings.Split(monorail_issue.Name, "/")
  id, err := strings.Atoi(name_parts[len(name_parts) - 1])
  if err != nil {
    return nil, fmt.Errorf("Atoi of %s: %v\n", name_parts[len(name_parts) - 1], err)
  }
  return &Issue{ Id: id }, nil
} 
