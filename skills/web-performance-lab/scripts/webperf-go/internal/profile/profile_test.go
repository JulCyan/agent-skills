package profile

import (
	"errors"
	"reflect"
	"testing"
)

func TestResolveReturnsVersionedProfiles(t *testing.T) {
	tests := []struct {
		name string
		want Profile
	}{
		{
			name: "desktop observed",
			want: Profile{
				Name:             "desktop-observed-v1",
				FormFactor:       "desktop",
				ThrottlingMethod: "provided",
				LighthouseArgs:   []string{"--form-factor=desktop", "--throttling-method=provided"},
			},
		},
		{
			name: "desktop lab",
			want: Profile{
				Name:             "desktop-lab-v1",
				FormFactor:       "desktop",
				ThrottlingMethod: "simulate",
				LighthouseArgs:   []string{"--form-factor=desktop", "--throttling-method=simulate"},
			},
		},
		{
			name: "mobile lab",
			want: Profile{
				Name:             "mobile-lab-v1",
				FormFactor:       "mobile",
				ThrottlingMethod: "simulate",
				LighthouseArgs:   []string{"--form-factor=mobile", "--throttling-method=simulate"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.want.Name)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("profile=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestResolveRejectsUnknownProfile(t *testing.T) {
	_, err := Resolve("desktop-v2")
	if !errors.Is(err, ErrUnknown) {
		t.Fatalf("err=%v", err)
	}
}

func TestListReturnsIndependentProfiles(t *testing.T) {
	profiles := List()
	if len(profiles) != 3 {
		t.Fatalf("len=%d", len(profiles))
	}
	profiles[0].LighthouseArgs[0] = "changed"
	if got := List()[0].LighthouseArgs[0]; got == "changed" {
		t.Fatalf("list exposed mutable profile arguments")
	}
}
