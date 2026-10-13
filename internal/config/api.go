package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type APIConfig struct {
	AuthPerLogin    int      `json:"auth_per_login"`
	AuthPerIP       int      `json:"auth_per_ip"`
	RequestsPerUser int      `json:"requests_per_user"`
	SyncPerClient   int      `json:"sync_per_client"`
	CORSOrigins     []string `json:"-"`
}

func (c APIConfig) Defaults() APIConfig {
	if c.AuthPerLogin == 0 {
		c.AuthPerLogin = 5
	}
	if c.AuthPerIP == 0 {
		c.AuthPerIP = 20
	}
	if c.RequestsPerUser == 0 {
		c.RequestsPerUser = 300
	}
	if c.SyncPerClient == 0 {
		c.SyncPerClient = 12
	}
	return c
}
func (c APIConfig) Validate() error {
	if c.AuthPerLogin < 1 || c.AuthPerIP < 1 || c.RequestsPerUser < 1 || c.SyncPerClient < 1 {
		return fmt.Errorf("API limits must be positive integers")
	}
	for _, origin := range c.CORSOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(origin, "*") || u.String() != origin {
			return fmt.Errorf("FREEFSM_API_CORS_ORIGINS requires exact HTTPS origins without paths, credentials or wildcards")
		}
	}
	return nil
}
func loadAPIConfig() (APIConfig, error) {
	c := APIConfig{}.Defaults()
	for key, target := range map[string]*int{"FREEFSM_AUTH_PER_LOGIN": &c.AuthPerLogin, "FREEFSM_AUTH_PER_IP": &c.AuthPerIP, "FREEFSM_API_PER_USER": &c.RequestsPerUser, "FREEFSM_API_SYNC_PER_CLIENT": &c.SyncPerClient} {
		if raw, ok := os.LookupEnv(key); ok {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 {
				return c, fmt.Errorf("%s must be a positive integer", key)
			}
			*target = v
		}
	}
	if raw := os.Getenv("FREEFSM_API_CORS_ORIGINS"); raw != "" {
		c.CORSOrigins = strings.Split(raw, ",")
	}
	return c, c.Validate()
}
