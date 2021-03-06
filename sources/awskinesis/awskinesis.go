package awskinesis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/kinesis"
	"github.com/aws/aws-sdk-go/service/kinesis/kinesisiface"
	"github.com/serverless/event-gateway-connector/connection"
	validator "gopkg.in/go-playground/validator.v9"
)

// AWSKinesis is a configuration used to configure AWS Kinesis stream as a source.
type AWSKinesis struct {
	StreamName         string `json:"streamName" validate:"required"`
	Region             string `json:"region" validate:"required"`
	AWSAccessKeyID     string `json:"awsAccessKeyId,omitempty"`
	AWSSecretAccessKey string `json:"awsSecretAccessKey,omitempty"`
	AWSSessionToken    string `json:"awsSessionToken,omitempty"`

	shards  []*kinesis.Shard
	service kinesisiface.KinesisAPI `validate:"-"`
}

func init() {
	connection.RegisterSource(connection.SourceType("awskinesis"), Load)
}

// Load will decode the provided JSON data into valid AWSKinesis format and establish
// a connection to the endpoint. Provided the connection is successful, we return an instance
// of the connection.Source, othewise an error.
func Load(data []byte) (connection.Source, error) {
	var src AWSKinesis
	err := json.Unmarshal(data, &src)
	if err != nil {
		return nil, fmt.Errorf("unable to load awskinesis source config: %s", err.Error())
	}

	if err := validator.New().Struct(src); err != nil {
		return nil, fmt.Errorf("missing required fields for awskinesis source: %s", err.Error())
	}

	conf := aws.NewConfig().WithRegion(src.Region)
	if src.AWSAccessKeyID != "" && src.AWSSecretAccessKey != "" {
		conf = conf.WithCredentials(
			credentials.NewStaticCredentials(
				src.AWSAccessKeyID,
				src.AWSSecretAccessKey,
				src.AWSSessionToken,
			),
		)
	}

	awsSession, err := session.NewSession(conf)
	if err != nil {
		return nil, fmt.Errorf("unable to create awskinesis service session: %s", err.Error())
	}

	src.service = kinesis.New(awsSession)

	stream, err := src.service.DescribeStream(
		&kinesis.DescribeStreamInput{
			StreamName: aws.String(src.StreamName),
		},
	)
	if err != nil {
		return src, err
	}
	sort.Slice(stream.StreamDescription.Shards, func(i, j int) bool {
		return *stream.StreamDescription.Shards[i].ShardId < *stream.StreamDescription.Shards[j].ShardId
	})
	src.shards = stream.StreamDescription.Shards

	return src, nil
}

// Fetch retrieves the next document from the awskinesis source
// Borrrowed some items from https://github.com/harlow/kinesis-consumer/blob/master/consumer.go#L251
func (a AWSKinesis) Fetch(ctx context.Context, shardID uint, lastSeq string) (*connection.Records, error) {
	ret := &connection.Records{LastSequence: lastSeq}
	params := &kinesis.GetShardIteratorInput{
		ShardId:           a.shards[shardID].ShardId,
		StreamName:        aws.String(a.StreamName),
		ShardIteratorType: aws.String("TRIM_HORIZON"),
	}

	if len(lastSeq) != 0 {
		params.ShardIteratorType = aws.String("AFTER_SEQUENCE_NUMBER")
		params.StartingSequenceNumber = aws.String(lastSeq)
	}

	// set up the shard iterator for our particular shardID
	iter, err := a.service.GetShardIteratorWithContext(ctx, params)
	if err != nil {
		return nil, err
	}

	records, err := a.service.GetRecordsWithContext(ctx, &kinesis.GetRecordsInput{
		ShardIterator: iter.ShardIterator,
	})
	if err != nil {
		return nil, err
	}

	for _, rec := range records.Records {
		ret.Data = append(ret.Data, rec.Data)
		ret.LastSequence = *rec.SequenceNumber
	}

	return ret, nil
}

// NumberOfWorkers returns number of shards to handle by the pool
func (a AWSKinesis) NumberOfWorkers() uint {
	return uint(len(a.shards))
}

// Close no-op.
func (a AWSKinesis) Close(_ uint) error {
	return nil
}
