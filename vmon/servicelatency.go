// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"time"

	"google.golang.org/api/cloudmonitoring/v2beta2"

	"v.io/x/devtools/internal/monitoring"
	"v.io/x/devtools/internal/runutil"
	"v.io/x/devtools/internal/testutil"
	"v.io/x/devtools/internal/tool"
)

var (
	timeout = 5 * time.Second
)

type prodService struct {
	name       string
	objectName string
}

// checkServiceLatency checks all services and adds their check latency to GCM.
func checkServiceLatency(ctx *tool.Context) error {
	services := []prodService{
		prodService{
			name:       "mounttable",
			objectName: namespaceRootFlag,
		},
		prodService{
			name:       "application repository",
			objectName: namespaceRootFlag + "/applications",
		},
		prodService{
			name:       "binary repository",
			objectName: namespaceRootFlag + "/binaries",
		},
		prodService{
			name:       "macaroon service",
			objectName: namespaceRootFlag + "/identity/" + blessingsRootFlag + "/root/macaroon",
		},
		prodService{
			name:       "google identity service",
			objectName: namespaceRootFlag + "/identity/" + blessingsRootFlag + "/root/google",
		},
		prodService{
			name:       "binary discharger",
			objectName: namespaceRootFlag + "/identity/" + blessingsRootFlag + "/root/discharger",
		},
		prodService{
			name:       "proxy service",
			objectName: namespaceRootFlag + "/proxy-mon/_debug",
		},
	}

	hasError := false
	for _, service := range services {
		if lat, err := checkSingleService(ctx, service); err != nil {
			testutil.Fail(ctx, "%s\n", service.name)
			fmt.Fprintf(ctx.Stderr(), "%v\n", err)
			hasError = true
		} else {
			testutil.Pass(ctx, "%s: %s\n", service.name, lat)
		}
	}
	if hasError {
		return fmt.Errorf("Failed to check some services.")
	}
	return nil
}

func checkSingleService(ctx *tool.Context, service prodService) (time.Duration, error) {
	// Check the given service and calculate the latency.
	vrpc := filepath.Join(binDirFlag, "vrpc")
	var bufErr bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdout = ioutil.Discard
	opts.Stderr = &bufErr
	latency := time.Duration(0)
	start := time.Now()
	if err := ctx.Run().TimedCommandWithOpts(timeout, opts, vrpc, "signature", "--insecure", service.objectName); err != nil {
		// When the command times out, use the "timeout" value as the check latency
		// without failing the check.
		// The GCM will have its own alert policy to handle abnormal check laency.
		// For example, GCM might decide to only send out alerts when latency is
		// over 500 ms for 10 minutes.
		if err == runutil.CommandTimedOutErr {
			latency = timeout
		} else {
			// Fail immediately on other errors (e.g. vrpc command errors).
			return 0, fmt.Errorf("%v: %s", err, bufErr.String())
		}
	} else {
		latency = time.Now().Sub(start)
	}

	// Add the latency as a custom metric to GCM.
	mdLat := monitoring.CustomMetricDescriptors["service-latency"]
	s, err := monitoring.Authenticate(serviceAccountFlag, keyFileFlag)
	if err != nil {
		return 0, err
	}
	timeStr := start.Format(time.RFC3339)
	_, err = s.Timeseries.Write(projectFlag, &cloudmonitoring.WriteTimeseriesRequest{
		Timeseries: []*cloudmonitoring.TimeseriesPoint{
			&cloudmonitoring.TimeseriesPoint{
				Point: &cloudmonitoring.Point{
					DoubleValue: float64(latency.Nanoseconds()) / 1000000.0,
					Start:       timeStr,
					End:         timeStr,
				},
				TimeseriesDesc: &cloudmonitoring.TimeseriesDescriptor{
					Metric: mdLat.Name,
					Labels: map[string]string{
						mdLat.Labels[0].Key: service.name,
					},
				},
			},
		},
	}).Do()
	if err != nil {
		return 0, fmt.Errorf("Timeseries Write failed: %v", err)
	}

	return latency, nil
}
