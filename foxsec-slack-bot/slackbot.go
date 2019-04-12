package foxsecslackbot

import (
	"bytes"
	"context"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mozilla-services/foxsec-pipeline-contrib/common"
	"github.com/mozilla-services/foxsec-pipeline-contrib/common/persons_api"

	"github.com/nlopes/slack"
	log "github.com/sirupsen/logrus"
	"go.mozilla.org/mozlogrus"
)

var (
	globalConfig            Config
	client                  *http.Client
	KEYNAME                 string
	PROJECT_ID              string
	FOXSEC_SLACK_CHANNEL_ID string
	DB                      *common.DBClient
)

const (
	EMAIL_CHAR_SET                     = "UTF-8"
	WHITELIST_IP_SLASH_COMMAND         = "/whitelist_ip"
	STAGING_WHITELIST_IP_SLASH_COMMAND = "/staging_whitelist_ip"
	DEFAULT_EXPIRATION_DURATION        = time.Hour * 24
	DURATION_DOC                       = "FoxsecBot uses Go's time.ParseDuration internally " +
		"with some custom checks. Examples: '72h' or '2h45m'. " +
		"Valid time units are 'm' and 'h'. If you omit a duration, " +
		"the default (24 hours) is used. If your duration is under 5 minutes, it is increased to 5 minutes."
)

func init() {
	mozlogrus.Enable("foxsec-slack-bot")
	client = &http.Client{
		Timeout: 10 * time.Second,
	}
	KEYNAME = os.Getenv("KMS_KEYNAME")
	PROJECT_ID = os.Getenv("GCP_PROJECT")
	FOXSEC_SLACK_CHANNEL_ID = os.Getenv("FOXSEC_SLACK_CHANNEL_ID")
	if FOXSEC_SLACK_CHANNEL_ID == "" {
		log.Fatal("Could not find FOXSEC_SLACK_CHANNEL_ID env var")
	}
	InitConfig()

	var err error
	DB, err = common.NewDBClient(context.Background(), PROJECT_ID)
	if err != nil {
		log.Errorf("Error creating db client: %s", err)
		return
	}
}

type Config struct {
	slackSigningSecret  string
	slackAuthToken      string
	slackClient         *slack.Client
	personsClientId     string
	personsClientSecret string
	personsClient       *persons_api.Client
	allowedGroups       []string
	sesClient           *common.SESClient
}

func InitConfig() {
	kms, err := common.NewKMSClient()
	if err != nil {
		log.Fatalf("Could not create kms client. Err: %s", err)
	}

	accessKeyId, err := kms.DecryptEnvVar(KEYNAME, "AWS_ACCESS_KEY_ID")
	if err != nil {
		log.Fatalf("Could not decrypt aws access key. Err: %s", err)
	}

	secretAccessKey, err := kms.DecryptEnvVar(KEYNAME, "AWS_SECRET_ACCESS_KEY")
	if err != nil {
		log.Fatalf("Could not decrypt aws secret access key. Err: %s", err)
	}

	globalConfig.sesClient, err = common.NewSESClient(os.Getenv("AWS_REGION"), accessKeyId, secretAccessKey, os.Getenv("SES_SENDER_EMAIL"), os.Getenv("ESCALATION_EMAIL"))
	if err != nil {
		log.Fatalf("Could not setup SESClient. Err: %s", err)
	}

	globalConfig.slackSigningSecret, err = kms.DecryptEnvVar(KEYNAME, "SLACK_SIGNING_SECRET")
	if err != nil {
		log.Fatalf("Could not decrypt slack signing secret. Err: %s", err)
	}

	globalConfig.slackAuthToken, err = kms.DecryptEnvVar(KEYNAME, "SLACK_AUTH_TOKEN")
	if err != nil {
		log.Fatalf("Could not decrypt slack auth token. Err: %s", err)
	}

	globalConfig.slackClient = slack.New(globalConfig.slackAuthToken)

	globalConfig.personsClientId, err = kms.DecryptEnvVar(KEYNAME, "PERSONS_CLIENT_ID")
	if err != nil {
		log.Fatalf("Could not decrypt persons client id. Err: %s", err)
	}

	globalConfig.personsClientSecret, err = kms.DecryptEnvVar(KEYNAME, "PERSONS_CLIENT_SECRET")
	if err != nil {
		log.Fatalf("Could not decrypt persons client secret. Err: %s", err)
	}

	globalConfig.personsClient, err = persons_api.NewClient(
		globalConfig.personsClientId,
		globalConfig.personsClientSecret,
		os.Getenv("PERSONS_BASE_URL"),
		os.Getenv("PERSONS_AUTH0_URL"),
	)
	if err != nil {
		log.Fatalf("Could not create persons api client: %s", err)
	}

	globalConfig.allowedGroups = strings.Split(os.Getenv("ALLOWED_LDAP_GROUPS"), ",")
}

func FoxsecSlackBot(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)

	err := verifySignature(r)
	if err != nil {
		log.Errorf("Error verifying signature: %s", err)
		return
	}

	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.Errorf("Error reading body: %v", err)
	}

	// And now set a new body, which will simulate the same data we read:
	r.Body = ioutil.NopCloser(bytes.NewBuffer(body))

	if cmd, err := slack.SlashCommandParse(r); err == nil && cmd.Command != "" {
		log.Infof("Command: %s", cmd.Command)
		if cmd.Command == WHITELIST_IP_SLASH_COMMAND || cmd.Command == STAGING_WHITELIST_IP_SLASH_COMMAND {
			resp, err := handleWhitelistCmd(r.Context(), cmd, DB)
			if err != nil {
				log.Errorf("error handling whitelist command: %s", err)
			}
			if resp != nil {
				err = sendSlackCallback(resp, cmd.ResponseURL)
				if err != nil {
					log.Errorf("error sending slack callback within slash command: %s", err)
					return
				}
			}
		}
	} else if callback, err := InteractionCallbackParse(body); err == nil {
		if isAlertConfirm(callback) {
			resp, err := handleAlertConfirm(r.Context(), callback, DB)
			if err != nil {
				log.Errorf("Error handling alert confirmation interaction: %s", err)
			}
			if resp != nil {
				err = sendSlackCallback(resp, callback.ResponseURL)
				if err != nil {
					log.Errorf("error sending slack callback for interaction: %s", err)
					return
				}
			}
		}
	}

	return
}
