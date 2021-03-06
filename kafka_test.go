package kfk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/stretchr/testify/require"

	. "github.com/chiguirez/kfk/v2"
)

const (
	broker  = "localhost:9092"
	groupID = "group-id"
)

type sentTestingMessage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

//nolint:funlen
func TestKafkaProduceAndConsume(t *testing.T) {
	var (
		kafkaConsumer *KafkaConsumer
		kafkaProducer *KafkaProducer
		err           error
	)

	stChan := make(chan sentTestingMessage)
	topicChan := make(chan string)
	topic := "topic-name"
	groupID := groupID

	kafkaBroker := os.Getenv("KAFKA_BROKER")
	if kafkaBroker == "" {
		kafkaBroker = broker
	}

	ctx, cancel := context.WithCancel(context.Background())

	setup := func(t *testing.T) (*KafkaProducer, func(t *testing.T)) {
		messageHandler := NewHandler(func(ctx context.Context, s sentTestingMessage) error {
			stChan <- s
			topicFromContext, _ := TopicFromContext(ctx)
			topicChan <- topicFromContext

			return nil
		})

		kafkaConsumer, err = NewKafkaConsumer(
			[]string{kafkaBroker},
			groupID,
			[]string{topic},
		)
		require.NoError(t, err)

		kafkaConsumer.AddHandler("sentTestingMessage", messageHandler)

		waitChan := make(chan struct{})

		go func() {
			err = kafkaConsumer.Start(ctx)
			require.NoError(t, err)
			waitChan <- struct{}{}
		}()

		kafkaProducer, err = NewKafkaProducer([]string{kafkaBroker})
		require.NoError(t, err)

		return kafkaProducer, func(t *testing.T) {
			cancel()
			<-waitChan

			config := sarama.NewConfig()
			config.Version = sarama.V1_1_0_0

			admin, err := sarama.NewClusterAdmin([]string{kafkaBroker}, config)
			require.NoError(t, err)

			err = admin.DeleteTopic(topic)
			require.NoError(t, err)

			// lets wait a few seconds for kafka to realize we are not consuming anymore
			time.Sleep(10 * time.Second)

			err = admin.DeleteConsumerGroup(groupID)
			require.NoError(t, err)
		}
	}

	t.Run("Given a producer and a message is sent to kafka topic", func(t *testing.T) {
		kafkaProducer, tearDown := setup(t)
		defer tearDown(t)

		msg := sentTestingMessage{
			ID:   "testing-message-id",
			Name: "testing-message-name",
		}
		err = kafkaProducer.Send(topic, msg.ID, msg)
		require.NoError(t, err)

		t.Run("When the consumer is started", func(t *testing.T) {
			t.Run("Then the message is consumed and the message information can be retrieved", func(t *testing.T) {
				message := <-stChan

				require.Equal(t, "testing-message-id", message.ID)
				require.Equal(t, "testing-message-name", message.Name)
			})

			t.Run("and Topic information can be retrieved too out of the context", func(t *testing.T) {
				require.Equal(t, topic, <-topicChan)
			})
		})

		t.Run("When check", func(t *testing.T) {
			t.Run("Then is ok", func(t *testing.T) {
				check := kafkaProducer.HealthCheck(context.Background())
				require.True(t, check)
			})
		})
	})
}

//nolint:funlen
func TestKafkaFallbackConsume(t *testing.T) {
	stChan := make(chan sentTestingMessage)
	topic := "topic-name-with-fallback"
	groupID := groupID

	kafkaBroker := os.Getenv("KAFKA_BROKER")
	if kafkaBroker == "" {
		kafkaBroker = broker
	}

	setup := func(t *testing.T) (*KafkaConsumer, *KafkaProducer, func(t *testing.T)) {
		ctx, cancel := context.WithCancel(context.Background())

		messageHandler := func(ctx context.Context, s []byte) error {
			message := &sentTestingMessage{}
			if err := json.Unmarshal(s, message); err != nil {
				return err
			}
			stChan <- *message

			return nil
		}

		kafkaConsumer, err := NewKafkaConsumer(
			[]string{kafkaBroker},
			groupID,
			[]string{topic},
		)
		require.NoError(t, err)

		kafkaConsumer.AddFallback(messageHandler)

		kafkaProducer, err := NewKafkaProducer([]string{kafkaBroker})
		require.NoError(t, err)

		waitChan := make(chan struct{})

		go func() {
			err := kafkaConsumer.Start(ctx)
			require.NoError(t, err)
			waitChan <- struct{}{}
		}()

		return kafkaConsumer, kafkaProducer, func(t *testing.T) {
			cancel()
			<-waitChan

			config := sarama.NewConfig()
			config.Version = sarama.V1_1_0_0

			admin, err := sarama.NewClusterAdmin([]string{kafkaBroker}, config)
			require.NoError(t, err)

			err = admin.DeleteTopic(topic)
			require.NoError(t, err)

			// lets wait a few seconds for kafka to realize we are not consuming anymore
			time.Sleep(10 * time.Second)

			err = admin.DeleteConsumerGroup(groupID)
			require.NoError(t, err)
		}
	}

	t.Run("Given a valid consumer and a message is sent to kafka topic", func(t *testing.T) {
		kafkaConsumer, kafkaProducer, tearDown := setup(t)
		defer tearDown(t)

		msg := sentTestingMessage{
			ID:   "testing-message-id",
			Name: "testing-message-name",
		}
		err := kafkaProducer.Send(topic, msg.ID, msg)
		require.NoError(t, err)

		t.Run("When the consumer is started", func(t *testing.T) {
			t.Run("Then the message is consumed by the fallback and the message information can be retrieved", func(t *testing.T) {
				message := <-stChan

				require.Equal(t, "testing-message-id", message.ID)
				require.Equal(t, "testing-message-name", message.Name)
			})
		})

		t.Run("When Checked then is ok", func(t *testing.T) {
			require.True(t, kafkaConsumer.HealthCheck(context.Background()))
		})
	})
}

type CustomEncodingDecodingMessage struct {
	id   string
	name string
}

func (c *CustomEncodingDecodingMessage) UnmarshallKFK(data []byte) error {
	s := string(data)
	split := strings.Split(s, ";")
	c.id = split[0]
	c.name = split[1]

	return nil
}

func (c CustomEncodingDecodingMessage) MarshalKFK() ([]byte, error) {
	customEncoding := fmt.Sprintf("%s;%s", c.id, c.name)

	return []byte(customEncoding), nil
}

func TestCustomEncodeDecode(t *testing.T) {
	topic := "topic-name-with-custom-encoding"
	groupID := groupID

	kafkaBroker := os.Getenv("KAFKA_BROKER")
	if kafkaBroker == "" {
		kafkaBroker = broker
	}

	config := sarama.NewConfig()

	config.Version = sarama.V1_0_0_0
	clusterAdmin, err := sarama.NewClusterAdmin([]string{kafkaBroker}, config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	kafkaConsumer, err := NewKafkaConsumer(
		[]string{kafkaBroker},
		groupID,
		[]string{topic},
	)
	require.NoError(t, err)

	messageChan := make(chan CustomEncodingDecodingMessage)

	kafkaConsumer.AddHandler(
		"CustomEncodingDecodingMessage",
		NewHandler(func(ctx context.Context, message CustomEncodingDecodingMessage) error {
			cancel()
			go func() {
				messageChan <- message
			}()

			return nil
		}))

	kafkaProducer, err := NewKafkaProducer([]string{kafkaBroker})
	require.NoError(t, err)

	t.Run("Given a message with Marshall and Unmarshall", func(t *testing.T) {
		message := CustomEncodingDecodingMessage{
			id:   "testing-message-id",
			name: "testing-message-name",
		}
		t.Run("When send using producer", func(t *testing.T) {
			err := kafkaProducer.Send(topic, message.id, message)
			require.NoError(t, err)
			t.Run("Then is received with custom changes from the coding/encoding", func(t *testing.T) {
				err := kafkaConsumer.Start(ctx)
				require.NoError(t, err)
				require.Equal(t, message, <-messageChan)
			})
		})
		_ = clusterAdmin.DeleteTopic(topic)
		_ = clusterAdmin.DeleteConsumerGroup(groupID)
	})
}
