package auth0pull

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"time"

	"github.com/mozilla-services/foxsec-pipeline-contrib/common"

	"cloud.google.com/go/datastore"
	stackdriver "cloud.google.com/go/logging"
	log "github.com/sirupsen/logrus"
	"go.mozilla.org/mozlogrus"
	"gopkg.in/auth0.v1/management"
)

const (
	LASTLOGID_KIND      = "last_log_id_auth0"
	LASTLOGID_KEY       = "last_log_id_auth0"
	LASTLOGID_NAMESPACE = "last_log_id_auth0"

	LOGGER_NAME = "auth0pull"
)

var (
	PROJECT_ID string

	config = &common.Configuration{}

	logClient         *management.LogManager
	datastoreClient   *datastore.Client
	stackdriverClient *stackdriver.Client
)

func init() {
	mozlogrus.Enable("auth0pull")
	PROJECT_ID = os.Getenv("GCP_PROJECT")
	InitConfig()
}

func InitConfig() {
	log.Info("Starting up...")
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		log.Fatal("$CONFIG_PATH must be set.")
	}
	err := config.LoadFrom(configPath)
	if err != nil {
		log.Fatalf("Could not load config file from `%s`: %s", configPath, err)
	}

	m, err := management.New(config.Auth0Domain, config.Auth0ClientId, config.Auth0ClientSecret)
	if err != nil {
		log.Fatalf("Could not create management client: %s", err)
	}
	logClient = management.NewLogManager(m)

	stackdriverClient, err = stackdriver.NewClient(context.Background(), PROJECT_ID)
	if err != nil {
		log.Fatalf("Could not create stackdriver client: %s", err)
	}

	datastoreClient, err = datastore.NewClient(context.Background(), PROJECT_ID)
	if err != nil {
		log.Fatalf("Could not create datastore client: %s", err)
	}
}

func getLogs(from string) ([]*management.Log, error) {
	var collectedLogs []*management.Log

	for {
		logs, err := logClient.List(func(v url.Values) {
			v.Set("from", from)
			v.Set("take", "100")
			v.Set("sort", "date:1")
		})
		if err != nil {
			return nil, err
		}
		if len(logs) == 0 {
			break
		}
		for _, l := range logs {
			collectedLogs = append(collectedLogs, l)
		}
		from = *logs[len(logs)-1].ID
	}
	return collectedLogs, nil
}

type lastLogId struct {
	LastLogId string    `json:"last_log_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

func loadLastLogId(ctx context.Context) (*lastLogId, error) {
	var (
		sf   common.StateField
		llid lastLogId
	)

	nk := datastore.NameKey(LASTLOGID_KIND, LASTLOGID_KEY, nil)
	nk.Namespace = LASTLOGID_NAMESPACE
	err := datastoreClient.Get(ctx, nk, &sf)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal([]byte(sf.State), &llid)
	if err != nil {
		return nil, err
	}

	return &llid, nil
}

func (llid *lastLogId) save(ctx context.Context) error {
	llid.UpdatedAt = time.Now()
	buf, err := json.Marshal(llid)
	if err != nil {
		return err
	}

	nk := datastore.NameKey(LASTLOGID_KIND, LASTLOGID_KEY, nil)
	nk.Namespace = LASTLOGID_NAMESPACE

	tx, err := datastoreClient.NewTransaction(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Put(nk, &common.StateField{State: string(buf)}); err != nil {
		return err
	}
	if _, err := tx.Commit(); err != nil {
		return err
	}

	return nil
}

// PubSubMessage is used for the function signature of the main function (Duopull())
// represent the data sent from PubSub. The data is not actually read, this is only
// used as a mechanism for triggering the function (using Cloud Scheduler or similiar)
type PubSubMessage struct {
	Data []byte `json:"data"`
}

func Auth0Pull(ctx context.Context, psmsg PubSubMessage) error {
	llid, err := loadLastLogId(ctx)
	if err != nil {
		log.Errorf("Error loading last log id: %s", err)
		return err
	}

	logs, err := getLogs(llid.LastLogId)
	if err != nil {
		log.Errorf("Error getting logs: %s", err)
		return err
	}

	logger := stackdriverClient.Logger(LOGGER_NAME)

	for _, log := range logs {
		logger.Log(stackdriver.Entry{Payload: log})
	}
	log.Infof("auth0pull logged %d entries", len(logs))

	err = logger.Flush()
	if err != nil {
		return err
	}

	llid.LastLogId = *logs[len(logs)-1].ID
	err = llid.save(ctx)
	if err != nil {
		return err
	}

	return nil
}
