// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test1

import "v.io/v23/context"

type Type1 struct{}

func (Type1) Method1() {
}

func (Type1) Method2(int) {
	//nologcall
}

func (Type1) Method3(ctx *context.T, b int) {
}
