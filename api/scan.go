// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/service"
)

// Scan hooks (module API 5, integration.json's scan part): the server walks a games library's
// folders and asks WittyPlay what the files are (identify) and what's known about each game
// (describe). The server keeps the items; what WittyPlay learns besides is in its own database.

// scanIdentify, scanDescribe and scanChoices are set by the files that answer them, in an init.
var (
	scanChoices  func(ctx context.Context, a *App, args wittymodule.ScanChoicesArgs) (wittymodule.ScanChoices, error)
	scanIdentify func(ctx context.Context, a *App, args wittymodule.ScanIdentifyArgs) (wittymodule.ScanIdentified, error)
	scanDescribe func(ctx context.Context, a *App, args wittymodule.ScanDescribeArgs) (wittymodule.ScanDescribed, error)
)

// ScanHooks answers the server's scan calls (service.Config.Scan).
func ScanHooks() *service.ScanHooks {
	return &service.ScanHooks{
		Choices: func(ctx context.Context, args wittymodule.ScanChoicesArgs) (wittymodule.ScanChoices, error) {
			a, err := Get()
			if err != nil || scanChoices == nil {
				return wittymodule.ScanChoices{}, err
			}
			return scanChoices(ctx, a, args)
		},
		Identify: func(ctx context.Context, args wittymodule.ScanIdentifyArgs) (wittymodule.ScanIdentified, error) {
			a, err := Get()
			if err != nil || scanIdentify == nil {
				return wittymodule.ScanIdentified{}, err
			}
			return scanIdentify(ctx, a, args)
		},
		Describe: func(ctx context.Context, args wittymodule.ScanDescribeArgs) (wittymodule.ScanDescribed, error) {
			a, err := Get()
			if err != nil {
				return wittymodule.ScanDescribed{}, err
			}
			noteDescribed(ctx, a, args) // the server's id for each game, for the game routes
			if scanDescribe == nil {
				return wittymodule.ScanDescribed{}, nil
			}
			return scanDescribe(ctx, a, args)
		},
	}
}
