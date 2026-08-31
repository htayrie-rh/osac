/*
Copyright (c) 2025 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the
License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific
language governing permissions and limitations under the License.
*/

package services

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestEnableAllIfNoneSet(t *testing.T) {
	tests := []struct {
		name     string
		initial  Flags
		expected Flags
	}{
		{
			name:    "all false enables all",
			initial: Flags{},
			expected: Flags{
				CaaS:  true,
				VMaaS: true,
				BMaaS: true,
				MaaS:  true,
			},
		},
		{
			name:    "one set preserves explicit flags",
			initial: Flags{CaaS: true, VMaaS: true},
			expected: Flags{
				CaaS:  true,
				VMaaS: true,
				BMaaS: false,
				MaaS:  false,
			},
		},
		{
			name:    "only BMaaS set preserves",
			initial: Flags{BMaaS: true},
			expected: Flags{
				CaaS:  false,
				VMaaS: false,
				BMaaS: true,
				MaaS:  false,
			},
		},
		{
			name:    "all already set remains unchanged",
			initial: Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true},
			expected: Flags{
				CaaS:  true,
				VMaaS: true,
				BMaaS: true,
				MaaS:  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.initial
			f.EnableAllIfNoneSet()
			if f != tt.expected {
				t.Errorf("got %+v, want %+v", f, tt.expected)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		flags   Flags
		wantErr bool
		errMsg  string
	}{
		{
			name:    "all enabled is valid",
			flags:   Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true},
			wantErr: false,
		},
		{
			name:    "CaaS with VMaaS is valid",
			flags:   Flags{CaaS: true, VMaaS: true},
			wantErr: false,
		},
		{
			name:    "CaaS with BMaaS is valid",
			flags:   Flags{CaaS: true, BMaaS: true},
			wantErr: false,
		},
		{
			name:    "VMaaS only is valid",
			flags:   Flags{VMaaS: true},
			wantErr: false,
		},
		{
			name:    "BMaaS only is valid",
			flags:   Flags{BMaaS: true},
			wantErr: false,
		},
		{
			name:    "CaaS without VMaaS or BMaaS is invalid",
			flags:   Flags{CaaS: true},
			wantErr: true,
			errMsg:  "CaaS requires at least one of VMaaS or BMaaS",
		},
		{
			name:    "CaaS with MaaS but no VMaaS or BMaaS is invalid",
			flags:   Flags{CaaS: true, MaaS: true},
			wantErr: true,
			errMsg:  "CaaS requires at least one of VMaaS or BMaaS",
		},
		{
			name:    "MaaS without CaaS is invalid",
			flags:   Flags{MaaS: true, VMaaS: true},
			wantErr: true,
			errMsg:  "MaaS requires CaaS",
		},
		{
			name:    "all disabled is valid after EnableAllIfNoneSet",
			flags:   Flags{},
			wantErr: false,
		},
		{
			name:    "MaaS alone is invalid for both rules",
			flags:   Flags{MaaS: true},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.flags
			if tt.name == "all disabled is valid after EnableAllIfNoneSet" {
				f.EnableAllIfNoneSet()
			}
			err := f.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error message %q should contain %q", err, tt.errMsg)
				}
			}
		})
	}
}

func TestRegisterFlags(t *testing.T) {
	t.Run("flags register and parse correctly", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		flags := RegisterFlags(fs)

		err := fs.Parse([]string{"--enable-caas", "--enable-vmaas"})
		if err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		if !flags.CaaS {
			t.Error("CaaS should be true")
		}
		if !flags.VMaaS {
			t.Error("VMaaS should be true")
		}
		if flags.BMaaS {
			t.Error("BMaaS should be false")
		}
		if flags.MaaS {
			t.Error("MaaS should be false")
		}
	})

	t.Run("no flags means all false", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		flags := RegisterFlags(fs)

		err := fs.Parse([]string{})
		if err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		if flags.CaaS || flags.VMaaS || flags.BMaaS || flags.MaaS {
			t.Errorf("all flags should be false by default, got %+v", flags)
		}
	})

	t.Run("all flags can be set", func(t *testing.T) {
		fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
		flags := RegisterFlags(fs)

		err := fs.Parse([]string{"--enable-caas", "--enable-vmaas", "--enable-bmaas", "--enable-maas"})
		if err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		if !flags.CaaS || !flags.VMaaS || !flags.BMaaS || !flags.MaaS {
			t.Errorf("all flags should be true, got %+v", flags)
		}
	})
}

func TestEnabledServices(t *testing.T) {
	tests := []struct {
		name     string
		flags    Flags
		expected []string
	}{
		{
			name:     "all enabled",
			flags:    Flags{CaaS: true, VMaaS: true, BMaaS: true, MaaS: true},
			expected: []string{"caas", "vmaas", "bmaas", "maas"},
		},
		{
			name:     "none enabled",
			flags:    Flags{},
			expected: []string{},
		},
		{
			name:     "partial",
			flags:    Flags{CaaS: true, BMaaS: true},
			expected: []string{"caas", "bmaas"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.flags.EnabledServices()
			if len(got) != len(tt.expected) {
				t.Errorf("EnabledServices() = %v, want %v", got, tt.expected)
				return
			}
			for i, v := range got {
				if v != tt.expected[i] {
					t.Errorf("EnabledServices()[%d] = %q, want %q", i, v, tt.expected[i])
				}
			}
		})
	}
}
