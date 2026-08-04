package mediasync

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type doctorReport struct {
	SchemaVersion    int                `json:"schema_version"`
	Command          string             `json:"command"`
	Status           string             `json:"status"`
	Runtime          doctorRuntime      `json:"runtime"`
	Config           doctorConfig       `json:"config"`
	Auth             doctorAuth         `json:"auth"`
	Capabilities     doctorCapabilities `json:"capabilities"`
	EndpointCheck    string             `json:"endpoint_check"`
	RemoteApplyReady bool               `json:"remote_apply_ready"`
	Problems         []string           `json:"problems"`
	NextAction       string             `json:"next_action"`
}

type doctorRuntime struct {
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

type doctorConfig struct {
	StoresConfigPath   string `json:"stores_config_path,omitempty"`
	Present            bool   `json:"present"`
	Valid              bool   `json:"valid"`
	SelectedStoreCount int    `json:"selected_store_count"`
}

type doctorAuth struct {
	Available bool   `json:"available"`
	Source    string `json:"source"`
}

type doctorCapabilities struct {
	LocalInput doctorCapability `json:"local_input"`
	SheetInput doctorCapability `json:"sheet_input"`
}

type doctorCapability struct {
	Available  bool   `json:"available"`
	Dependency string `json:"dependency,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

func runDoctor(stdout io.Writer, opts commandOptions) error {
	report := buildDoctorReport(opts)
	if opts.format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	fmt.Fprintf(stdout, "status=%s remote_apply_ready=%t\n", report.Status, report.RemoteApplyReady)
	fmt.Fprintf(stdout, "config_present=%t auth_available=%t auth_source=%s endpoint_check=%s\n", report.Config.Present, report.Auth.Available, report.Auth.Source, report.EndpointCheck)
	fmt.Fprintf(stdout, "local_input_available=%t sheet_input_available=%t\n", report.Capabilities.LocalInput.Available, report.Capabilities.SheetInput.Available)
	for _, problem := range report.Problems {
		fmt.Fprintf(stdout, "problem=%s\n", problem)
	}
	fmt.Fprintf(stdout, "next_action=%s\n", report.NextAction)
	return nil
}

func buildDoctorReport(opts commandOptions) doctorReport {
	report := doctorReport{
		SchemaVersion: 1,
		Command:       "doctor",
		Status:        "READY",
		Runtime: doctorRuntime{
			GoVersion: runtime.Version(),
			OS:        runtime.GOOS,
			Arch:      runtime.GOARCH,
		},
		Config: doctorConfig{
			StoresConfigPath: opts.storesConfig,
		},
		Capabilities:  diagnoseInputCapabilities(),
		EndpointCheck: "NOT_RUN",
		Problems:      []string{},
	}

	var selectedStores []Store
	if opts.storesConfig == "" {
		report.Problems = append(report.Problems, "stores config not found; pass --stores-config <path>")
	} else if info, err := os.Stat(opts.storesConfig); err != nil || info.IsDir() {
		report.Problems = append(report.Problems, "stores config is not a readable file")
	} else {
		report.Config.Present = true
		cfg, err := loadStoresConfig(opts.storesConfig)
		if err != nil {
			report.Problems = append(report.Problems, "stores config is invalid: "+err.Error())
		} else if selectedStores, err = selectStores(cfg, opts.stores); err != nil {
			report.Problems = append(report.Problems, "store selection is invalid: "+err.Error())
		} else {
			report.Config.Valid = true
			report.Config.SelectedStoreCount = len(selectedStores)
		}
	}

	var authProblems []string
	if opts.envFile != "" {
		values, err := readDotEnv(opts.envFile)
		if err != nil {
			report.Auth = doctorAuth{Source: "invalid_env_file"}
			authProblems = []string{"explicit env file is not readable"}
		} else {
			report.Auth, authProblems = diagnoseAuthWithLookup(selectedStores, func(key string) string {
				return values[key]
			}, "_env_file")
		}
	} else {
		report.Auth, authProblems = diagnoseAuth(selectedStores)
	}
	if !report.Auth.Available {
		report.Problems = append(report.Problems, authProblems...)
	}

	report.RemoteApplyReady = report.Config.Valid && report.Auth.Available
	if !report.RemoteApplyReady {
		report.Status = "NEEDS_SETUP"
		report.NextAction = "provide the reported config/auth prerequisites, then rerun --json doctor"
	} else {
		report.NextAction = "run plan, review the explicit plan, then run apply preview"
	}
	return report
}

func diagnoseInputCapabilities() doctorCapabilities {
	sheet := doctorCapability{Available: true, Dependency: "lark-cli"}
	if _, err := exec.LookPath(sheet.Dependency); err != nil {
		sheet.Available = false
		sheet.NextAction = "install and authenticate lark-cli only when Sheet input is required"
	}
	return doctorCapabilities{
		LocalInput: doctorCapability{Available: true},
		SheetInput: sheet,
	}
}

func diagnoseAuth(stores []Store) (doctorAuth, []string) {
	return diagnoseAuthWithLookup(stores, os.Getenv, "_env")
}

func diagnoseAuthWithLookup(stores []Store, lookup func(string) string, sourceSuffix string) (doctorAuth, []string) {
	clientIDPresent := strings.TrimSpace(lookup("SHOPIFY_CLIENT_ID")) != ""
	clientSecretPresent := strings.TrimSpace(lookup("SHOPIFY_CLIENT_SECRET")) != ""
	if clientIDPresent != clientSecretPresent {
		return doctorAuth{Source: "partial_client_credentials" + sourceSuffix}, []string{"SHOPIFY_CLIENT_ID and SHOPIFY_CLIENT_SECRET must be configured together"}
	}

	if len(stores) == 0 {
		if clientIDPresent {
			return doctorAuth{Available: true, Source: "client_credentials" + sourceSuffix}, nil
		}
		if strings.TrimSpace(lookup("SHOPIFY_ADMIN_TOKEN")) != "" {
			return doctorAuth{Available: true, Source: "admin_token" + sourceSuffix}, nil
		}
		return doctorAuth{Source: "missing"}, []string{"Shopify auth is unavailable in the selected source"}
	}

	sources := map[string]bool{}
	for _, store := range stores {
		suffix := envSuffix(store.ID)
		storeClientIDPresent := strings.TrimSpace(lookup("SHOPIFY_CLIENT_ID_"+suffix)) != ""
		storeClientSecretPresent := strings.TrimSpace(lookup("SHOPIFY_CLIENT_SECRET_"+suffix)) != ""
		if storeClientIDPresent != storeClientSecretPresent {
			return doctorAuth{Source: "partial_per_store_client_credentials" + sourceSuffix}, []string{"per-store client ID and client secret must be configured together for " + store.ID}
		}
		switch {
		case storeClientIDPresent:
			sources["per_store_client_credentials"+sourceSuffix] = true
		case clientIDPresent:
			sources["client_credentials"+sourceSuffix] = true
		case strings.TrimSpace(lookup("SHOPIFY_ADMIN_TOKEN_"+suffix)) != "":
			sources["per_store_admin_token"+sourceSuffix] = true
		case strings.TrimSpace(lookup("SHOPIFY_ADMIN_TOKEN")) != "":
			sources["admin_token"+sourceSuffix] = true
		default:
			return doctorAuth{Source: "missing"}, []string{"Shopify auth is unavailable for selected store " + store.ID}
		}
	}
	if len(sources) == 1 {
		for source := range sources {
			return doctorAuth{Available: true, Source: source}, nil
		}
	}
	return doctorAuth{Available: true, Source: "mixed_env"}, nil
}
