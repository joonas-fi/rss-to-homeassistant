package homeassistant

import (
	"encoding/json"
	"fmt"

	"github.com/function61/gokit/log/logex"
	"github.com/yosssi/gmq/mqtt"
	"github.com/yosssi/gmq/mqtt/client"
)

type InboundCommand struct {
	EntityId string
	State    string
}

// sends data to Home Assistant over a MQTT broker
type MqttClient struct {
	mqtt *client.Client
	logl *logex.Leveled
}

func NewMqttClient(
	mqttAddr string,
	logl *logex.Leveled,
) (*MqttClient, error) {
	mqttClient := client.New(&client.Options{
		ErrorHandler: func(err error) {
			logl.Error.Printf("mqtt: %s", err)
		},
	})

	if err := mqttClient.Connect(&client.ConnectOptions{
		Network:  "tcp",
		Address:  mqttAddr,
		ClientID: []byte("Hautomo-Home-Assistant"),
	}); err != nil {
		return nil, err
	}

	return &MqttClient{
		mqtt: mqttClient,
		logl: logl,
	}, nil
}

func (h *MqttClient) Close() error {
	h.mqtt.Terminate()
	return nil
}

func (h *MqttClient) PublishAttributes(entity *Entity, attributes map[string]string) error {
	if len(entity.mqttAttributesTopic()) == 0 {
		return fmt.Errorf("PublishAttributes: no attribute topic for %s", entity.Id)
	}

	attributesJson, err := json.Marshal(attributes)
	if err != nil {
		return err
	}

	if err := h.mqtt.Publish(&client.PublishOptions{
		QoS:       mqtt.QoS0,
		Retain:    false,
		TopicName: entity.mqttAttributesTopic(),
		Message:   attributesJson,
	}); err != nil {
		return fmt.Errorf("PublishAttributes: %w", err)
	}

	return nil
}

// https://www.home-assistant.io/docs/mqtt/discovery/
func (h *MqttClient) AutodiscoverEntities(entities ...*Entity) error {
	for _, entity := range entities {
		if err := h.mqtt.Publish(&client.PublishOptions{
			QoS:       mqtt.QoS0,
			Retain:    false,
			TopicName: entity.mqttDiscoveryTopic(),
			Message:   entity.mqttDiscoveryMsg(),
		}); err != nil {
			return fmt.Errorf("AutodiscoverSensors: %w", err)
		}
	}

	return nil
}
