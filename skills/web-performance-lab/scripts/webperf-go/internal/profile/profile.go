// Package profile contains compiled, versioned measurement profiles.
package profile

import (
	"errors"
	"fmt"
)

var ErrUnknown = errors.New("unknown profile")

type Profile struct {
	Name             string   `json:"name"`
	FormFactor       string   `json:"formFactor"`
	ThrottlingMethod string   `json:"throttlingMethod"`
	LighthouseArgs   []string `json:"lighthouseArgs"`
}

var profiles = []Profile{
	{
		Name:             "desktop-observed-v1",
		FormFactor:       "desktop",
		ThrottlingMethod: "provided",
		LighthouseArgs:   []string{"--form-factor=desktop", "--throttling-method=provided"},
	},
	{
		Name:             "desktop-lab-v1",
		FormFactor:       "desktop",
		ThrottlingMethod: "simulate",
		LighthouseArgs:   []string{"--form-factor=desktop", "--throttling-method=simulate"},
	},
	{
		Name:             "mobile-lab-v1",
		FormFactor:       "mobile",
		ThrottlingMethod: "simulate",
		LighthouseArgs:   []string{"--form-factor=mobile", "--throttling-method=simulate"},
	},
}

func List() []Profile {
	result := make([]Profile, len(profiles))
	for i, item := range profiles {
		result[i] = clone(item)
	}
	return result
}

func Resolve(name string) (Profile, error) {
	for _, item := range profiles {
		if item.Name == name {
			return clone(item), nil
		}
	}
	return Profile{}, fmt.Errorf("%w: %s", ErrUnknown, name)
}

func clone(item Profile) Profile {
	item.LighthouseArgs = append([]string(nil), item.LighthouseArgs...)
	return item
}
