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

package triton

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"time"

	promlog "github.com/prometheus/common/log"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/config"
	"golang.org/x/net/context"
)

const (
	tritonLabel             = model.MetaLabelPrefix + "triton_"
	tritonLabelMachineId    = tritonLabel + "machine_id"
	tritonLabelMachineAlias = tritonLabel + "machine_alias"
	tritonLabelMachineImage = tritonLabel + "machine_image"
	tritonLabelServerId     = tritonLabel + "server_id"
)

type TritonDiscoveryResponse struct {
	Containers []struct {
		ServerUUID  string `json:"server_uuid"`
		VMAlias     string `json:"vm_alias"`
		VMImageUUID string `json:"vm_image_uuid"`
		VMUUID      string `json:"vm_uuid"`
	} `json:"containers"`
}

// TritonDiscovery periodically performs Triton-SD requests. It implements
// the TargetProvider interface.
type TritonDiscovery struct {
	sdConfig *config.TritonSDConfig
	client   *http.Client
	interval time.Duration
}

// NewTritonDiscovery returns a new TritonDiscovery which periodically refreshes its targets.
func NewDiscovery(conf *config.TritonSDConfig) *TritonDiscovery {
	cert, err := tls.LoadX509KeyPair(conf.Cert, conf.Key)
	if err != nil {
		log.Fatalln("Unable to load cert", err)
	}

	clientCACert, err := ioutil.ReadFile(conf.Cert)
	if err != nil {
		log.Fatal("Unable to open cert", err)
	}

	clientCertPool := x509.NewCertPool()
	clientCertPool.AppendCertsFromPEM(clientCACert)

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            clientCertPool,
		InsecureSkipVerify: conf.InsecureSkipVerify,
	}

	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := &http.Client{Transport: transport}

	return &TritonDiscovery{
		sdConfig: conf,
		client:   client,
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
	var endpoint = fmt.Sprintf("https://%s:%d/v%d/discover", td.sdConfig.Endpoint, td.sdConfig.Port, td.sdConfig.Version)
	tg := &config.TargetGroup{
		Source: endpoint,
	}

	resp, err := td.client.Get(endpoint)
	if err != nil {
		return tg, fmt.Errorf("Error reaching endpoint: %s", err)
	}
	defer resp.Body.Close()

	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return tg, fmt.Errorf("Error reading response body: %s", err)
	}
	tdr := TritonDiscoveryResponse{}
	json.Unmarshal([]byte(string(data)), &tdr)
	for _, container := range tdr.Containers {
		labels := model.LabelSet{
			tritonLabelMachineId:    model.LabelValue(container.VMUUID),
			tritonLabelMachineAlias: model.LabelValue(container.VMAlias),
			tritonLabelMachineImage: model.LabelValue(container.VMImageUUID),
			tritonLabelServerId:     model.LabelValue(container.ServerUUID),
		}
		addr := fmt.Sprintf("%s.%s:%d", container.VMUUID, td.sdConfig.DnsSuffix, td.sdConfig.Port)
		labels[model.AddressLabel] = model.LabelValue(addr)
		tg.Targets = append(tg.Targets, labels)
	}

	return tg, nil
}