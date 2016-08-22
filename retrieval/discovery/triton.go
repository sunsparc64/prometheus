// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package discovery

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/joyent/gocommon/client"
	"github.com/joyent/gosdc/cloudapi"
	"github.com/joyent/gosign/auth"
	promlog "github.com/prometheus/common/log"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/util/strutil"
	"golang.org/x/net/context"
)

const (
	tritonLabel            = model.MetaLabelPrefix + "triton_"
	tritonLabelMachineId   = tritonLabel + "machine_id"
	tritonLabelMachineName = tritonLabel + "machine_name"
	tritonLabelPublicIP    = tritonLabel + "public_ip"
	tritonLabelTag         = tritonLabel + "tag_"
)

// TritonDiscovery periodically performs Triton-SD requests. It implements
// the TargetProvider interface.
type TritonDiscovery struct {
	sdConfig  *config.TritonSDConfig
	api *cloudapi.Client
	interval  time.Duration
}

// NewTritonDiscovery returns a new TritonDiscovery which periodically refreshes its targets.
func NewTritonDiscovery(conf *config.TritonSDConfig) *TritonDiscovery {
	// TODO: What to do with 'err' here?
	tritonAuth, err := auth.NewAuth(conf.Account, conf.Key, conf.KeyAlgorithm)
	if (err != nil) {
		promlog.Errorf("Invalid settings for account %s - %s", conf.Account, err)
		return nil
	}

    tritonCreds := &auth.Credentials{
        UserAuthentication: tritonAuth,
        SdcKeyId:           conf.KeyId,
        SdcEndpoint:        auth.Endpoint{URL: conf.Url},
    }

    api := cloudapi.New(client.NewClient(
        conf.Url,
        cloudapi.DefaultAPIVersion,
        tritonCreds,
		// XXX: Something better?
		log.New(os.Stderr, "", log.LstdFlags),
    ))

	return &TritonDiscovery{
		sdConfig: conf,
		api: api,
		interval: time.Duration(conf.RefreshInterval),
	}
}

// Run implements the TargetProvider interface.
func (td *TritonDiscovery) Run(ctx context.Context, ch chan<- []*config.TargetGroup) {
	defer close(ch)

	ticker := time.NewTicker(td.interval)
	defer ticker.Stop()

	// Get an initial set right away.
	tg, err := td.refresh()
	if err != nil {
		promlog.Error(err)
	} else {
		ch <- []*config.TargetGroup{tg}
	}

	for {
		select {
		case <-ticker.C:
			tg, err := td.refresh()
			if err != nil {
				promlog.Error(err)
			} else {
				ch <- []*config.TargetGroup{tg}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (td *TritonDiscovery) refresh() (*config.TargetGroup, error) {
	u, err := url.Parse(td.sdConfig.Url)
	if err != nil {
		panic(err)
	}

	tg := &config.TargetGroup{
		Source: td.sdConfig.Url,
	}

	// TODO: Filter to only running containers?
	filter := cloudapi.NewFilter()
	filter.Add("state", "running")

	machines, err := td.api.ListMachines(filter)
	if (err != nil) {
		return nil, fmt.Errorf("could not list machines: %s", err)
	}

	for _, machine := range machines {
		// TODO: Should we keep non running containers?
		if machine.State != "running" {
			continue
		}

		labels := model.LabelSet{
			tritonLabelMachineId:   model.LabelValue(machine.Id),
			tritonLabelMachineName: model.LabelValue(machine.Name),
			tritonLabelPublicIP:    model.LabelValue(machine.PrimaryIP),
		}

		// TODO: Get the correct URL for this.
		labels[model.AddressLabel] = model.LabelValue(u.Host)

		// TODO: CNS name for container?

		for k, v := range machine.Tags {
			name := strutil.SanitizeLabelName(k)
			labels[tritonLabelTag+model.LabelName(name)] = model.LabelValue(fmt.Sprintf("%s", v))
		}
		tg.Targets = append(tg.Targets, labels)
	}

	return tg, nil
}
