package handlers

import (
	"context"

	"dplaned/internal/middleware"
)

func contextWithUser(ctx context.Context, u *middleware.User) context.Context {
	return context.WithValue(ctx, middleware.UserContextKey, u)
}
