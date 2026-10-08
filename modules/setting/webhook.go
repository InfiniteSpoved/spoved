// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"context"
	"net/url"
	"slices"

	"forgejo.org/modules/log"
)

// WebhookTypesState is stored as JSON in the "webhook.types" system setting. It lists the disabled types, so an
// instance without a stored value (fresh or upgraded) has every type enabled.
type WebhookTypesState struct {
	Disabled []string `json:"disabled"`
}

func (s WebhookTypesState) Enabled(hookType string) bool {
	return !slices.Contains(s.Disabled, hookType)
}

func IsWebhookTypeEnabled(ctx context.Context, hookType string) bool {
	return Config().WebhookTypes.Value(ctx).Enabled(hookType)
}

// Webhook settings
var Webhook = struct {
	QueueLength        int
	DeliverTimeout     int
	SkipTLSVerify      bool
	AllowedHostList    string
	PagingNum          int
	ProxyURL           string
	ProxyURLFixed      *url.URL
	ProxyHosts         []string
	PayloadCommitLimit int
}{
	QueueLength:        1000,
	DeliverTimeout:     5,
	SkipTLSVerify:      false,
	PagingNum:          10,
	ProxyURL:           "",
	ProxyHosts:         []string{},
	PayloadCommitLimit: 15,
}

func loadWebhookFrom(rootCfg ConfigProvider) {
	sec := rootCfg.Section("webhook")
	Webhook.QueueLength = sec.Key("QUEUE_LENGTH").MustInt(1000)
	Webhook.DeliverTimeout = sec.Key("DELIVER_TIMEOUT").MustInt(5)
	Webhook.SkipTLSVerify = sec.Key("SKIP_TLS_VERIFY").MustBool()
	Webhook.AllowedHostList = sec.Key("ALLOWED_HOST_LIST").MustString("")
	Webhook.PagingNum = sec.Key("PAGING_NUM").MustInt(10)
	Webhook.ProxyURL = sec.Key("PROXY_URL").MustString("")
	if Webhook.ProxyURL != "" {
		var err error
		Webhook.ProxyURLFixed, err = url.Parse(Webhook.ProxyURL)
		if err != nil {
			log.Error("Webhook PROXY_URL is not valid")
			Webhook.ProxyURL = ""
		}
	}
	Webhook.ProxyHosts = sec.Key("PROXY_HOSTS").Strings(",")
	Webhook.PayloadCommitLimit = sec.Key("PAYLOAD_COMMIT_LIMIT").MustInt(15)
}
