package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kinesis"
	"github.com/aws/aws-sdk-go/service/s3"

	stackdriver "cloud.google.com/go/logging"
	"cloud.google.com/go/pubsub"
	"google.golang.org/api/option"

	log "github.com/sirupsen/logrus"
	"go.mozilla.org/mozlogrus"
	"go.mozilla.org/sops"
	"go.mozilla.org/sops/decrypt"
)

func init() {
	if os.Getenv("CT_DEBUG_LOGGING") == "1" {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	mozlogrus.Enable("cloudtrail-streamer")
}

var (
	globalConfig Config
)

const GZIP_CONTENT_TYPE = "application/x-gzip"

type Config struct {
	awsKinesisStream    string // Kinesis stream for event submission
	awsKinesisRegion    string // AWS region the kinesis stream exists in
	awsKinesisBatchSize int    // Number of records in a batched put to the kinesis stream
	awsS3RoleArn        string // Optional Role to assume for S3 operations
	eventType           string // Whether to use the S3 or SNS event handler. Default is S3.

	awsKinesisClient *kinesis.Kinesis
	awsSession       *session.Session

	gcpProjectId       string // GCP Project Id for where the PubSub Topic or Stackdriver logger live
	gcpTopicId         string // GCP PubSub Topic Id
	gcpStackdriverName string // GCP Stackdriver name

	pubsubClient      *pubsub.Client
	stackdriverClient *stackdriver.Client

	eventFilters []*EventFilter
}

func (c *Config) getGcpCredentials() ([]byte, error) {
	// TODO: Set this as an env var?
	path := "./gcp_credentials.json"

	log.Debugf("Accessing gcp credentials from %s", path)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	credentials, err := decrypt.Data(data, "json")
	if err != nil {
		if err.Error() == sops.MetadataNotFound.Error() {
			// not an encrypted file
			credentials = data
		} else {
			return nil, err
		}
	}

	return credentials, nil
}

func (c *Config) init() error {
	// Global
	c.awsSession = session.Must(session.NewSession())
	c.awsS3RoleArn = os.Getenv("CT_S3_ROLE_ARN")

	c.eventType = "S3"
	eventType := os.Getenv("CT_EVENT_TYPE")
	if eventType != "" {
		c.eventType = eventType
	}
	if c.eventType != "S3" && c.eventType != "SNS" {
		return fmt.Errorf("CT_EVENT_TYPE is set to an invalid value, %s, must be either 'S3' or 'SNS'", eventType)
	}

	filters := os.Getenv("CT_EVENT_FILTERS")
	if filters != "" {
		c.eventFilters = parseFilters(filters)
	}

	c.awsKinesisStream = os.Getenv("CT_KINESIS_STREAM")
	c.gcpTopicId = os.Getenv("CT_TOPIC_ID")
	c.gcpStackdriverName = os.Getenv("CT_STACKDRIVER_NAME")
	if c.awsKinesisStream == "" && c.gcpTopicId == "" && c.gcpStackdriverName == "" {
		return fmt.Errorf("At least one of CT_KINESIS_STREAM, CT_TOPIC_ID, or CT_STACKDRIVER_NAME must be set")
	}

	c.awsKinesisRegion = os.Getenv("CT_KINESIS_REGION")
	c.awsKinesisBatchSize = 500
	batchSize := os.Getenv("CT_KINESIS_BATCH_SIZE")

	if c.awsKinesisStream != "" {
		if c.awsKinesisRegion == "" {
			return fmt.Errorf("CT_KINESIS_REGION must be set")
		}

		if batchSize != "" {
			var err error
			c.awsKinesisBatchSize, err = strconv.Atoi(batchSize)
			if err != nil {
				log.Fatalf("Error converting CT_KINESIS_BATCH_SIZE (%v) to int: %s", batchSize, err)
			}

			if c.awsKinesisBatchSize <= 0 || c.awsKinesisBatchSize > 500 {
				return fmt.Errorf("CT_KINESIS_BATCH_SIZE must be set to a value inbetween 1 and 500")
			}
		}

		c.awsKinesisClient = kinesis.New(
			c.awsSession,
			aws.NewConfig().WithRegion(c.awsKinesisRegion),
		)
	}

	c.gcpProjectId = os.Getenv("CT_PROJECT_ID")

	if c.gcpTopicId != "" || c.gcpStackdriverName != "" {
		if c.gcpProjectId == "" {
			return fmt.Errorf("CT_PROJECT_ID must be set")
		}

		gcpPubSubCredentials, err := c.getGcpCredentials()
		if err != nil {
			log.Fatalf("Error getting GCP credentials. Err: %s", err)
		}

		if c.gcpTopicId != "" {
			c.pubsubClient, err = pubsub.NewClient(context.Background(), c.gcpProjectId, option.WithCredentialsJSON(gcpPubSubCredentials))
			if err != nil {
				log.Fatalf("Error creating pubsubClient. Err: %s", err)
			}
		}
		if c.gcpStackdriverName != "" {
			c.stackdriverClient, err = stackdriver.NewClient(context.Background(), c.gcpProjectId, option.WithCredentialsJSON(gcpPubSubCredentials))
			if err != nil {
				log.Fatalf("Error creating stackdriverClient. Err: %s", err)
			}
		}
	}

	return nil
}

type CloudTrailFile struct {
	Records []map[string]interface{} `json:"Records"`
}

type EventFilter struct {
	EventName   string
	EventSource string
}

func parseFilters(filters string) []*EventFilter {
	var eventFilters []*EventFilter
	for _, filter := range strings.Split(filters, ",") {
		event_filter := strings.Split(filter, ":")
		if len(event_filter) != 2 {
			continue
		}
		eventFilters = append(eventFilters, newEventFilter(event_filter[0], event_filter[1]))
	}
	return eventFilters
}

func newEventFilter(source, name string) *EventFilter {
	return &EventFilter{EventName: name, EventSource: fmt.Sprintf("%s.amazonaws.com", source)}
}

func (ef *EventFilter) DoesMatch(record map[string]interface{}) bool {
	return record["eventName"] == ef.EventName || record["eventSource"] == ef.EventSource
}

func doFiltersMatch(record map[string]interface{}) bool {
	for _, ef := range globalConfig.eventFilters {
		if ef.DoesMatch(record) {
			return true
		}
	}
	return false
}

func fetchLogFromS3(s3Client *s3.S3, bucket string, objectKey string) (*s3.GetObjectOutput, error) {
	logInput := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectKey),
	}

	object, err := s3Client.GetObject(logInput)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			log.Errorf("AWS Error: %s", aerr)
			return nil, aerr
		}
		log.Errorf("Error getting S3 object: %s", err)
		return nil, err
	}

	return object, nil
}

func readLogFile(object *s3.GetObjectOutput) (*CloudTrailFile, error) {
	defer object.Body.Close()

	var logFileBlob io.ReadCloser
	var err error
	if object.ContentType != nil && *object.ContentType == GZIP_CONTENT_TYPE {
		logFileBlob, err = gzip.NewReader(object.Body)
		if err != nil {
			log.Errorf("Error unzipping cloudtrail json file: %s", err)
			return nil, err
		}
		defer logFileBlob.Close()
	} else {
		logFileBlob = object.Body
	}

	blobBuf := new(bytes.Buffer)
	_, err = blobBuf.ReadFrom(logFileBlob)
	if err != nil {
		log.Errorf("Error reading from logFileBlob: %s", err)
		return nil, err
	}

	var logFile CloudTrailFile
	err = json.Unmarshal(blobBuf.Bytes(), &logFile)
	if err != nil {
		log.Errorf("Error unmarshalling s3 object to CloudTrailFile: %s", err)
		return nil, err
	}

	return &logFile, nil
}

type Streamer struct {
	kinesisStreamer     *KinesisStreamer
	pubsubStreamer      *PubSubStreamer
	stackdriverStreamer *StackdriverStreamer
	quit                chan int
}

func NewStreamer() *Streamer {
	s := &Streamer{quit: make(chan int)}

	if globalConfig.awsKinesisStream != "" {
		s.kinesisStreamer = NewKinesisStreamer()
	}

	if globalConfig.gcpTopicId != "" {
		s.pubsubStreamer = NewPubSubStreamer(globalConfig.pubsubClient)
	}

	if globalConfig.gcpStackdriverName != "" {
		s.stackdriverStreamer = NewStackdriverStreamer(globalConfig.stackdriverClient)
	}

	return s
}

func (s *Streamer) Close() {
	log.Info("Closing streamers")
	if s.kinesisStreamer != nil {
		s.kinesisStreamer.Close()
	}
	if s.pubsubStreamer != nil {
		s.pubsubStreamer.Close()
	}
	if s.stackdriverStreamer != nil {
		s.stackdriverStreamer.Close()
	}
	s.quit <- 0
}

func (s *Streamer) streamInBackground() {
	if s.kinesisStreamer != nil {
		go s.kinesisStreamer.Stream()
	}
	<-s.quit
}

func (s *Streamer) Stream(awsRegion string, bucket string, objectKey string) error {
	s3ClientConfig := aws.NewConfig().WithRegion(awsRegion)
	if globalConfig.awsS3RoleArn != "" {
		creds := stscreds.NewCredentials(globalConfig.awsSession, globalConfig.awsS3RoleArn)
		s3ClientConfig.Credentials = creds
	}
	s3Client := s3.New(globalConfig.awsSession, s3ClientConfig)

	log.Debugf("Reading %s from %s with client config of %+v", objectKey, bucket, s3Client.Config)

	object, err := fetchLogFromS3(s3Client, bucket, objectKey)
	if err != nil {
		return err
	}

	logFile, err := readLogFile(object)
	if err != nil {
		return err
	}

	s.streamToServices(logFile)

	return nil
}

func (s *Streamer) streamToServices(logfile *CloudTrailFile) {
	for _, record := range logfile.Records {
		if doFiltersMatch(record) {
			continue
		}

		log.Debugf("Writing record to streams: %v", record)
		encodedRecord, err := json.Marshal(record)
		if err != nil {
			log.Errorf("Error marshalling record (%v) to json: %s", record, err)
			continue
		}

		if s.kinesisStreamer != nil {
			s.kinesisStreamer.Send(encodedRecord)
		}
		if s.pubsubStreamer != nil {
			s.pubsubStreamer.Send(encodedRecord)
		}
		if s.stackdriverStreamer != nil {
			s.stackdriverStreamer.Send(encodedRecord)
		}
	}
}

type KinesisStreamer struct {
	wg   *sync.WaitGroup
	ch   chan string
	quit chan int
}

func NewKinesisStreamer() *KinesisStreamer {
	ks := &KinesisStreamer{
		wg:   &sync.WaitGroup{},
		ch:   make(chan string),
		quit: make(chan int),
	}
	return ks
}

func (k *KinesisStreamer) Stream() error {
	var kRecordsBuf []*kinesis.PutRecordsRequestEntry
	k.wg.Add(1)
	defer k.wg.Done()

	for {
		select {
		case r := <-k.ch:
			record := []byte(r)
			kRecordsBuf = append(kRecordsBuf, &kinesis.PutRecordsRequestEntry{
				Data:         record,
				PartitionKey: aws.String("key"),
			})

			if len(kRecordsBuf) > 0 && len(kRecordsBuf)%globalConfig.awsKinesisBatchSize == 0 {
				log.Infof("PutRecords to kinesis with a len of %d", len(kRecordsBuf))
				_, err := globalConfig.awsKinesisClient.PutRecords(&kinesis.PutRecordsInput{
					Records:    kRecordsBuf,
					StreamName: aws.String(globalConfig.awsKinesisStream),
				})
				if err != nil {
					log.Errorf("Error pushing records to kinesis: %s", err)
					return err
				}

				kRecordsBuf = kRecordsBuf[:0]
			}

		case <-k.quit:
			if len(kRecordsBuf) != 0 {
				log.Infof("PutRecords to kinesis with a len of %d", len(kRecordsBuf))
				_, err := globalConfig.awsKinesisClient.PutRecords(&kinesis.PutRecordsInput{
					Records:    kRecordsBuf,
					StreamName: aws.String(globalConfig.awsKinesisStream),
				})
				if err != nil {
					log.Errorf("Error pushing records to kinesis: %s", err)
					return err
				}
			}
			return nil
		}
	}
}

func (k *KinesisStreamer) Close() {
	k.quit <- 0
	k.wg.Wait()
	return
}

func (k *KinesisStreamer) Send(record []byte) {
	k.ch <- string(record)
}

type PubSubStreamer struct {
	topic *pubsub.Topic
}

func NewPubSubStreamer(client *pubsub.Client) *PubSubStreamer {
	t := client.Topic(globalConfig.gcpTopicId)
	t.PublishSettings = pubsub.PublishSettings{CountThreshold: 300}
	return &PubSubStreamer{topic: t}
}

func (ps *PubSubStreamer) Send(record []byte) {
	ps.topic.Publish(context.TODO(), &pubsub.Message{Data: record})
}

func (ps *PubSubStreamer) Close() {
	ps.topic.Stop()
}

type StackdriverStreamer struct {
	logger *stackdriver.Logger
}

func NewStackdriverStreamer(client *stackdriver.Client) *StackdriverStreamer {
	l := client.Logger(globalConfig.gcpStackdriverName)
	return &StackdriverStreamer{logger: l}
}

func (s *StackdriverStreamer) Send(record []byte) {
	s.logger.Log(stackdriver.Entry{Payload: json.RawMessage(record)})
}

func (s *StackdriverStreamer) Close() {
	err := s.logger.Flush()
	if err != nil {
		log.Errorf("Error flushing stackdriver logger. Err: %s", err)
	}
}

func S3Handler(ctx context.Context, s3Event events.S3Event) error {
	log.Infof("Handling S3 event: %v", s3Event)

	streamer := NewStreamer()
	go streamer.streamInBackground()
	defer streamer.Close()

	for _, s3Record := range s3Event.Records {
		err := streamer.Stream(
			s3Record.AWSRegion,
			s3Record.S3.Bucket.Name,
			s3Record.S3.Object.Key,
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func SNSHandler(ctx context.Context, snsEvent events.SNSEvent) error {
	log.Infof("Handling SNS event: %+v", snsEvent)

	for _, snsRecord := range snsEvent.Records {
		var s3Event events.S3Event
		err := json.Unmarshal([]byte(snsRecord.SNS.Message), &s3Event)
		if err != nil {
			return err
		}

		err = S3Handler(ctx, s3Event)
		if err != nil {
			return err
		}
	}

	return nil
}

func main() {
	log.Info("Starting cloudtrail-streamer")
	err := globalConfig.init()
	if err != nil {
		log.Fatalf("Invalid config (%v): %s", globalConfig, err)
	}

	log.Debugf("Running with filters: %v", globalConfig.eventFilters)

	if globalConfig.eventType == "S3" {
		log.Debug("Starting S3Handler")
		lambda.Start(S3Handler)
	} else if globalConfig.eventType == "SNS" {
		log.Debug("Starting SNSHandler")
		lambda.Start(SNSHandler)
	} else {
		log.Fatalf("eventType (%s) is not set to either S3 or SNS.", globalConfig.eventType)
	}
}
