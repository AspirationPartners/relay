package relay

import (
	"bytes"
	"fmt"
	"github.com/streadway/amqp"
)

// Consumer is a type that is used only for consuming messages from a single queue.
// Multiple Consumers can multiplex a single relay
type Consumer struct {
	conf        *Config
	consName    string
	queue       string
	channel     *amqp.Channel
	deliverChan <-chan amqp.Delivery
	lastMsg     uint64 // Last delivery tag, used for Ack
	needAck     bool
}

// Consume will consume the next available message. The
// message must be acknowledged with Ack() or Nack() before
// the next call to Consume unless EnableMultiAck is true.
func (c *Consumer) Consume(out interface{}) error {
	// Check if we are closed
	if c.channel == nil {
		return ChannelClosed
	}

	// Check if an ack is required
	if c.needAck && !c.conf.EnableMultiAck {
		return fmt.Errorf("Ack required before consume!")
	}

	// Wait for a message
	d, ok := <-c.deliverChan
	if !ok {
		return ChannelClosed
	}

	// Store the delivery tag for future Ack
	c.lastMsg = d.DeliveryTag
	c.needAck = true

	// Decode the message
	buf := bytes.NewBuffer(d.Body)
	if err := c.conf.Serializer.RelayDecode(buf, out); err != nil {
		return fmt.Errorf("Failed to decode message! Got: %s", err)
	}
	return nil
}

// ConsumeAck will consume the next message and acknowledge
// that the message has been received. This prevents the message
// from being redelivered, and no call to Ack() or Nack() is needed.
func (c *Consumer) ConsumeAck(out interface{}) error {
	if err := c.Consume(out); err != nil {
		return err
	}
	if err := c.Ack(); err != nil {
		return err
	}
	return nil
}

// Ack will send an acknowledgement to the server that the
// last message returned by Consume was processed. If EnableMultiAck is true, then all messages up to the last consumed one will
// be acknowledged
func (c *Consumer) Ack() error {
	if c.channel == nil {
		return ChannelClosed
	}
	if !c.needAck {
		fmt.Errorf("Ack is not required!")
	}
	if err := c.channel.Ack(c.lastMsg, c.conf.EnableMultiAck); err != nil {
		return err
	}
	c.needAck = false
	return nil
}

// Nack will send a negative acknowledgement to the server that the
// last message returned by Consume was not processed and should be
// redelivered. If EnableMultiAck is true, then all messages up to
// the last consumed one will be negatively acknowledged
func (c *Consumer) Nack() error {
	if c.channel == nil {
		return ChannelClosed
	}
	if !c.needAck {
		fmt.Errorf("Nack is not required!")
	}
	if err := c.channel.Nack(c.lastMsg,
		c.conf.EnableMultiAck, true); err != nil {
		return err
	}
	c.needAck = false
	return nil
}

// Close will shutdown the Consumer. Any messages that are still
// in flight will be Nack'ed.
func (c *Consumer) Close() error {
	// Make sure close is idempotent
	if c.channel == nil {
		return nil
	}
	defer func() {
		c.channel = nil
	}()

	// Stop consuming inputs
	if err := c.channel.Cancel(c.consName, false); err != nil {
		return fmt.Errorf("Failed to stop consuming! Got: %s", err)
	}

	// Wait to read all the pending messages
	var lastMsg uint64
	var needAck bool
	for {
		d, ok := <-c.deliverChan
		if !ok {
			break
		}
		lastMsg = d.DeliveryTag
		needAck = true
	}

	// Send a Nack for all these messages
	if needAck {
		if err := c.channel.Nack(lastMsg, true, true); err != nil {
			return fmt.Errorf("Failed to send Nack for inflight messages! Got: %s", err)
		}
	}

	// Shutdown the channel
	return c.channel.Close()
}