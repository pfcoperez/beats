// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package application

import (
	"time"

	"github.com/elastic/beats/x-pack/agent/pkg/core/logger"
	"github.com/elastic/beats/x-pack/agent/pkg/fleetapi"
	"github.com/elastic/beats/x-pack/agent/pkg/scheduler"
)

type dispatcher interface {
	Dispatch(...action) error
}

type agentInfo interface {
	AgentID() string
}

type fleetReporter interface {
	Events() ([]fleetapi.SerializableEvent, func())
}

// fleetGateway is a gateway between the Agent and the Fleet API, it's take cares of all the
// bidirectional communication requirements. The gateway aggregates events and will periodically
// call the API to send the events and will receive actions to be executed locally.
// The only supported action for now is a "ActionPolicyChange".
type fleetGateway struct {
	log        *logger.Logger
	dispatcher dispatcher
	client     clienter
	scheduler  scheduler.Scheduler
	agentInfo  agentInfo
	reporter   fleetReporter
	done       chan struct{}
}

type fleetGatewaySettings struct {
	Duration time.Duration
	Jitter   time.Duration
}

func newFleetGateway(
	log *logger.Logger,
	settings *fleetGatewaySettings,
	agentInfo agentInfo,
	client clienter,
	d dispatcher,
	r fleetReporter,
) (*fleetGateway, error) {
	scheduler := scheduler.NewPeriodicJitter(settings.Duration, settings.Jitter)
	return newFleetGatewayWithScheduler(
		log,
		settings,
		agentInfo,
		client,
		d,
		scheduler,
		r,
	)
}

func newFleetGatewayWithScheduler(
	log *logger.Logger,
	settings *fleetGatewaySettings,
	agentInfo agentInfo,
	client clienter,
	d dispatcher,
	scheduler scheduler.Scheduler,
	r fleetReporter,
) (*fleetGateway, error) {
	return &fleetGateway{
		log:        log,
		dispatcher: d,
		client:     client,
		agentInfo:  agentInfo, //TODO(ph): this need to be a struct.
		scheduler:  scheduler,
		done:       make(chan struct{}),
		reporter:   r,
	}, nil
}

func (f *fleetGateway) worker() {
	for {
		select {
		case <-f.scheduler.WaitTick():
			f.log.Debug("FleetGateway calling Checkin API")
			resp, err := f.execute()
			if err != nil {
				f.log.Error(err)
				continue
			}

			actions := make([]action, len(resp.Actions))
			for idx, a := range resp.Actions {
				actions[idx] = a
			}

			if err := f.dispatcher.Dispatch(actions...); err != nil {
				f.log.Error(err)
			}

			f.log.Debug("FleetGateway sleeping")
		case <-f.done:
			return
		}
	}
}

func (f *fleetGateway) execute() (*fleetapi.CheckinResponse, error) {
	// get events
	ee, ack := f.reporter.Events()

	// checkin
	cmd := fleetapi.NewCheckinCmd(f.agentInfo, f.client)
	req := &fleetapi.CheckinRequest{
		Events: ee,
	}

	resp, err := cmd.Execute(req)
	if err != nil {
		return nil, err
	}

	// ack events so they are dropped from queue
	ack()
	return resp, nil
}

func (f *fleetGateway) Start() {
	go f.worker()
}

func (f *fleetGateway) Stop() {
	close(f.done)
	f.scheduler.Stop()
}
