package amqp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
    "net"

	"github.com/elastic/libbeat/common"
	"github.com/elastic/libbeat/logp"
	"github.com/elastic/libbeat/outputs"

	amqp "github.com/streadway/amqp"
)

func init() {
	outputs.RegisterOutputPlugin("amqp", AmqpOutputPlugin{})
}

type AmqpOutputPlugin struct{}

func (f AmqpOutputPlugin) NewOutput(
	beat string,
	config outputs.MothershipConfig,
	topology_expire int,
) (outputs.Outputer, error) {
	output := &amqpOutput{}
	err := output.Init(beat, config, topology_expire)
	if err != nil {
		return nil, err
	}
	return output, nil
}

type amqpOutput struct {
	Index string
	Chan  *amqp.Channel

	ReconnectInterval  time.Duration
	AmqpUrl            string
    heartbeat          int
	Exchange           *AmqpExchange
	Timeout            time.Duration

	sendingQueue chan RedisQueueMsg
	connected    bool
}

type AmqpExchange struct {
	Name        string
	Kind        string
    Durable     bool
    AutoDelete  bool
    Internal    bool
    NoWait          bool
    DeliveryMode    int
}

type message struct {
	index string
	msg   string
}

func (out *amqpOutput) Init(beat string, config outputs.MothershipConfig, topology_expire int) error {

	out.AmqpUrl = config.AmqpUrl

	out.Exchange = &AmqpExchange{
        Kind: "direct",
        Durable: true,
    }
	if config.Exchange != nil {
		out.Exchange = config.Exchange
	}

	out.Timeout = 5 * time.Second
	if config.Timeout != 0 {
		out.Timeout = time.Duration(config.Timeout) * time.Second
	}

    out.Heartbeat = 580 * time.Second
    if config.Heartbeat != 0 {
        out.Heartbeat = time.Duration(config.Heartbeat) * time.Second
    }

	if config.Index != "" {
		out.Index = config.Index
	} else {
		out.Index = beat
	}

	out.ReconnectInterval = time.Duration(1) * time.Second
	if config.Reconnect_interval != 0 {
		out.ReconnectInterval = time.Duration(config.Reconnect_interval) * time.Second
	}


	logp.Info("[AmqpOutput] Using Amqp server %s", out.AmqpUrl)
	logp.Info("[AmqpOutput] Amqp connection timeout %s", out.Timeout)
	logp.Info("[AmqpOutput] Amqp reconnect interval %s", out.ReconnectInterval)
	logp.Info("[AmqpOutput] Using key pattern %s", out.Index)
	logp.Info("[AmqpOutput] Using exchange %v", out.Exchange)

	out.sendingQueue = make(chan AmqpQueueMsg, 1000)

	out.Reconnect()
	go out.SendMessagesGoroutine()

	return nil
}

func (out *amqpOutput) AmqpConnect(out amqpOutput) (amqp.Channel, error) {
	var conn *amqp.Connection
    var channel *amqp.Channel
    var err error

    conn, err = amqp.DialConfig( out.AmqpUrl, amqp.Config{
        Dial: func(network, addr string) (net.Conn, error) {
            return net.DialTimeout(network, addr, out.Timeout)
        },
        Heartbeat: out.Heartbeat,
    })
	if err != nil {
		return nil, err
	}

    channel, err = conn.Channel()
    if err != nil {
        return nil, err
    }

    err = channel.ExchangeDeclare(out.Exchange.Name, out.Exchange.Kind, out.Exchange.Durable, out.Exchange.AutoDelete, out.Exchange.Internal, out.Exchange.NoWait, nil)
    if err != nil {
        return nil, err
    }

	return channel, nil
}

func (out *amqpOutput) Connect() error {
	var err error
	out.Chan, err = out.AmqpConnect(out)
	if err != nil {
		return err
	}
	out.connected = true

	return nil
}

func (out *amqpOutput) Close() {
	out.Conn.Close()
}

func (out *amqpOutput) SendMessagesGoroutine() {

	var err error
	
	for {
		select {
		case queueMsg := <-out.sendingQueue:

			if !out.connected {
				logp.Debug("output_amqp", "Droping pkt ...")
				continue
			}
			logp.Debug("output_amqp", "Send event to amqp")
			
    		err = out.Chan.Publish(
                    out.Exchange.Name,
                    queueMsg.index,
                    false,
                    false,
                    amqp.Publishing{
                        ContentType: "application/json",
                        ContentEncoding: "UTF-8",
                        DeliveryMode: out.Exchange.DeliveryMode,
                        Body: queueMsg.msg
                    },
                )
			if err != nil {
				logp.Err("Fail to publish event to AMQP: %s", err)
				out.connected = false
				go out.Reconnect()
			}
        }
	}
}

func (out *amqpOutput) Reconnect() {

	for {
		err := out.Connect()
		if err != nil {
			logp.Warn("Error connecting to Amqp (%s). Retrying in %s", err, out.ReconnectInterval)
			time.Sleep(out.ReconnectInterval)
		} else {
			break
		}
	}
}

func (out *amqpOutput) PublishIPs(name string, localAddrs []string) error {
    // not supported by this output type
    return nil
}

func (out *amqpOutput) GetNameByIP(ip string) string {
    // not supported by this output type
    return ""
}

func (out *amqpOutput) PublishEvent(ts time.Time, event common.MapStr) error {

	json_event, err := json.Marshal(event)
	if err != nil {
		logp.Err("Fail to convert the event to JSON: %s", err)
		return err
	}

	out.sendingQueue <- AmqpQueueMsg{index: out.Index, msg: string(json_event)}

	logp.Debug("output_amqp", "Publish event")
	return nil
}
