package auth

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	PermKnowledgeRead  = "knowledge:read"
	PermKnowledgeWrite = "knowledge:write"
	PermPlannerRead    = "planner:read"
	PermPlannerWrite   = "planner:write"
	PermNotifyRead     = "notify:read"
	PermUsersAdmin     = "users:admin"

	RoleUser  = "user"
	RoleAdmin = "admin"
)

var rolePerms = map[string][]string{
	RoleUser: {
		PermKnowledgeRead, PermKnowledgeWrite,
		PermPlannerRead, PermPlannerWrite,
		PermNotifyRead,
	},
	RoleAdmin: {
		PermKnowledgeRead, PermKnowledgeWrite,
		PermPlannerRead, PermPlannerWrite,
		PermNotifyRead, PermUsersAdmin,
	},
}

type Claims struct {
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	Perms []string `json:"perms"`
	jwt.RegisteredClaims
}

func PermsForRoles(roles []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, role := range roles {
		for _, p := range rolePerms[role] {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	return out
}

func Issue(secret, userID, email, name string, roles []string, ttl time.Duration) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("jwt secret must be at least 32 characters")
	}
	now := time.Now()
	claims := Claims{
		Email: email,
		Name:  name,
		Roles: roles,
		Perms: PermsForRoles(roles),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			ID:        uuid.NewString(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "atlas-user",
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

func Parse(secret, token string) (*Claims, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, errors.New("missing token")
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

func HasPerm(perms []string, need string) bool {
	for _, p := range perms {
		if p == need {
			return true
		}
	}
	return false
}
