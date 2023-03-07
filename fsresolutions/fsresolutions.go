package fsresolutions

import (
  "context"
  "fmt"
  "time"

  "cloud.google.com/go/firestore"
  "google.golang.org/api/iterator"
  "google.golang.org/grpc/codes"
  "google.golang.org/grpc/status"
)

const (
  // The latest version of the data. If we change FSResolutionData,
  // this version might need to increase
  Version = "1.1"

  lastRunTimeDoc = "last_run"
)

// TODO(vmpstr): These need a good rename and a data wipe to support
// other repos like open-ui
type FSResolutionData struct {
  // Version of the data
  Version string               `firestore:"version,omitempty"`
  // The crbug id assosicated with this resolution if any
  CrbugId int                  `firestore:"crbug-id,omitempty"`
  // The github issue id in csswg-drafts repo
  CsswgDraftsId int            `firestore:"csswg-drafts-id,omitempty"`
  // The github issue id in the csswg-resolutions repo
  CsswgResolutionsId int       `firestore:"csswg-resolutions-id,omitempty"`
  // Comment ids in csswg-drafts repo that recorded these resolutions
  ResolutionCommentIds []int64 `firestore:"resolution-comment-ids,omitempty"`
  // True if there is a pending triage event
  HasPendingTriageEvents bool  `firestore:"has-pending-triage-events,omitempty"`
  // Comment ids in csswg-resolutions repo that were processed for triage
  TriagedCommentIds []int64    `firestore:"triaged-comment-ids,omitempty"`
}

type Client struct {
  fsCollection string
  client *firestore.Client
}

//-------------------- new client / close  --------------------
func NewClient(project, collection string) (*Client, error) {
  client, err := firestore.NewClient(context.Background(), project)
  if err != nil {
    return nil, fmt.Errorf("firestore.NewClient: %v", err)
  }

  return &Client{
    fsCollection: collection,
    client: client,
  }, nil
}

func (c *Client) Close() {
  if c.client != nil {
    c.client.Close()
  }
}

//-------------------- LoadDataBy*  --------------------
func (c *Client) LoadDataByDocName(name string) (*FSResolutionData, error) {
  if c.client == nil {
    return nil, fmt.Errorf("No firestore client")
  }

  docsnap, err := c.client.Collection(c.fsCollection).Doc(name).
      Get(context.Background())
  if err != nil {
    if status.Code(err) == codes.NotFound {
      return nil, nil
    }
    return nil, fmt.Errorf("get: %v", err)
  }

  var data FSResolutionData
  if err = docsnap.DataTo(&data); err != nil {
    return nil, fmt.Errorf("doc.DataTo: %v", err)
  }
  return &data, nil
}

func (c *Client) LoadDataByCsswgResolutionsId(number int) (
    *FSResolutionData, error) {
  if c.client == nil {
    return nil, fmt.Errorf("No firestore client")
  }

  query := c.client.Collection(c.fsCollection).Where(
      "csswg-resolutions-id", "==", number)
  return loadDataFromQuery(query)
}

func (c *Client) LoadDataByCsswgDraftsId(number int) (
    *FSResolutionData, error) {
  if c.client == nil {
    return nil, fmt.Errorf("No firestore client")
  }

  query := c.client.Collection(c.fsCollection).Where(
      "csswg-drafts-id", "==", number)
  return loadDataFromQuery(query)
}

func loadDataFromQuery(query firestore.Query) (*FSResolutionData, error) {
  iter := query.Documents(context.Background())
  doc, err := iter.Next()
  if err != nil {
    if err == iterator.Done {
      return &FSResolutionData{ Version: Version }, nil
    }
    return nil, fmt.Errorf("iter.Next: %v", err)
  }

  var data FSResolutionData
  err = doc.DataTo(&data)
  if err != nil {
    return nil, fmt.Errorf("doc.DataTo: %v", err)
  }
  return &data, nil
}

//-------------------- set / updates --------------------
func (c *Client) SetData(name string, data *FSResolutionData) error {
  if c.client == nil {
    return fmt.Errorf("No firestore client")
  }

  // Always update empty version, as a convenience
  if data.Version == "" {
    data.Version = Version
  }

  if _, err := c.client.Collection(c.fsCollection).Doc(name).Set(
      context.Background(), data); err != nil {
    return fmt.Errorf("set: %v", err)
  }
  return nil
}

func (c *Client) UpdateDataSetResolutionCommentIds(
    name string, data *FSResolutionData) error {
  return c.updateDataSetUpdate(name, []firestore.Update{
    { Path: "resolution-comment-ids", Value: data.ResolutionCommentIds }})
}

func (c *Client) UpdateDataSetCrbugId(
    name string, data *FSResolutionData) error {
  return c.updateDataSetUpdate(name, []firestore.Update{
    { Path: "crbug-id", Value: data.CrbugId }})
}

func (c *Client) UpdateDataSetHasPendingTriageEvents(
    name string, data *FSResolutionData) error {
  return c.updateDataSetUpdate(name, []firestore.Update{
    { Path: "has-pending-triage-events", Value: data.HasPendingTriageEvents }})
}

func (c *Client) updateDataSetUpdate(
    name string, updates []firestore.Update) error {
  if c.client == nil {
    return fmt.Errorf("No firestore client")
  }

  if _, err := c.client.Collection(c.fsCollection).Doc(name).Update(
      context.Background(), updates); err != nil {
    return fmt.Errorf("update: %v", err)
  }
  return nil
}

//-------------------- last run time --------------------
func (c *Client) LoadLastRunTime() (time.Time, error) {
  if c.client == nil {
    return time.Time{}, fmt.Errorf("No firestore client")
  }

  docsnap, err := c.client.Collection(c.fsCollection).Doc(lastRunTimeDoc).Get(
      context.Background())
  if err != nil {
    return time.Time{}, fmt.Errorf("get: %v", err)
  }

  var data struct { Time time.Time `firestore:"time"` }
  if err = docsnap.DataTo(&data); err != nil {
    return time.Time{}, fmt.Errorf("docsnap.DataTo: %v", err)
  }
  return data.Time, nil
}

func (c *Client) UpdateLastRunTime(t time.Time) error {
  if c.client == nil {
    return fmt.Errorf("No firestore client")
  }

  type Data struct {
    Time time.Time `firestore:"time"`
  }
  data := &Data{ Time: t }

  if _, err := c.client.Collection(c.fsCollection).Doc(lastRunTimeDoc).Set(
      context.Background(), data); err != nil {
    return fmt.Errorf("set: %v", err)
  }
  return nil
}
