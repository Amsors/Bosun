package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 领域错误。handler 层据此映射到 spec/03 的 API 错误码。
var (
	// ErrEmailTaken 表示邮箱已被注册（store 层返回）。
	ErrEmailTaken = errors.New("email already registered")
	// ErrUserNotFound 表示用户不存在（store 层返回）。
	ErrUserNotFound = errors.New("user not found")
	// ErrRefreshTokenNotFound 表示 refresh token hash 未命中（store 层返回）。
	ErrRefreshTokenNotFound = errors.New("refresh token not found")

	// ErrInvalidCredentials 表示邮箱/密码或 refresh token 无效，映射 20001。
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrRefreshExpired 表示 refresh token 已过期，映射 20002。
	ErrRefreshExpired = errors.New("refresh token expired")
	// ErrValidation 表示请求字段不合法，映射 10001。
	ErrValidation = errors.New("validation failed")
)

// User 是认证域的用户实体。
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	CreatedAt    time.Time
	DisabledAt   *time.Time
}

// RefreshTokenRecord 是 refresh token 的持久化视图（只含 hash，不含明文）。
type RefreshTokenRecord struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	FamilyID   uuid.UUID
	TokenHash  []byte
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *uuid.UUID
}

// IdempotencyRecord 是幂等键的持久化视图。
type IdempotencyRecord struct {
	Scope       uuid.UUID
	Key         string
	Method      string
	Path        string
	RequestHash []byte
	Status      int
	Body        []byte
	ExpiresAt   time.Time
}

// Store 抽象认证相关的持久化操作，便于对 service 做纯逻辑单测。
type Store interface {
	CreateUser(ctx context.Context, id uuid.UUID, email, passwordHash string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
	GetUserByID(ctx context.Context, id uuid.UUID) (User, error)
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error
	ListUserIDs(ctx context.Context) ([]string, error)

	InsertRefreshToken(ctx context.Context, rec RefreshTokenRecord) error
	GetRefreshTokenByHash(ctx context.Context, hash []byte) (RefreshTokenRecord, error)
	// RotateRefreshToken 原子撤销旧 token 并在胜出时插入替换 token，返回是否胜出。
	RotateRefreshToken(ctx context.Context, oldID uuid.UUID, replacement RefreshTokenRecord) (bool, error)
	RevokeRefreshTokenFamily(ctx context.Context, familyID uuid.UUID) error
}

// IdempotencyStore serializes and persists create-request responses for 24-hour replay.
type IdempotencyStore interface {
	WithIdempotencyLock(ctx context.Context, scope uuid.UUID, key string, fn func() error) error
	GetIdempotencyKey(ctx context.Context, scope uuid.UUID, key string) (*IdempotencyRecord, error)
	InsertIdempotencyKey(ctx context.Context, rec IdempotencyRecord) (bool, error)
}

// EnvProvisioner 抽象用户环境 CR 的创建与状态读取。
type EnvProvisioner interface {
	Ensure(ctx context.Context, userID string) error
	Phase(ctx context.Context, userID string) (string, error)
}

// Service 实现注册、登录、刷新、登出与当前用户查询的业务逻辑。
type Service struct {
	store      Store
	issuer     *JWTIssuer
	env        EnvProvisioner
	argon      Argon2Params
	refreshTTL time.Duration
	dummyHash  string
	now        func() time.Time
	logger     *slog.Logger
}

// Config 汇总 Service 的依赖与参数。
type Config struct {
	Store           Store
	Issuer          *JWTIssuer
	Env             EnvProvisioner
	Argon2          Argon2Params
	RefreshTokenTTL time.Duration
	Now             func() time.Time
	Logger          *slog.Logger
}

// NewService 构造认证 service，并预计算一个 dummy 哈希用于恒定时间的失败登录。
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Issuer == nil || cfg.Env == nil {
		return nil, errors.New("auth service requires store, issuer and env provisioner")
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	// dummy 哈希让「用户不存在」与「密码错误」两条路径耗时相近，抑制邮箱枚举时序侧信道。
	dummy, err := HashPassword("bosun-dummy-password", cfg.Argon2)
	if err != nil {
		return nil, fmt.Errorf("precompute dummy hash: %w", err)
	}
	return &Service{
		store:      cfg.Store,
		issuer:     cfg.Issuer,
		env:        cfg.Env,
		argon:      cfg.Argon2,
		refreshTTL: cfg.RefreshTokenTTL,
		dummyHash:  dummy,
		now:        now,
		logger:     logger,
	}, nil
}

// RegisterResult 是注册结果。
type RegisterResult struct {
	User             User
	EnvironmentPhase string
}

// TokenResult 是登录/刷新成功后返回给 handler 的凭据集合。
type TokenResult struct {
	User             User
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// Register 校验输入、以 argon2id 保存用户，并在提交后幂等创建 UserEnvironment CR。
func (s *Service) Register(ctx context.Context, email, password string) (*RegisterResult, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password, s.argon)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate user id: %w", err)
	}
	user, err := s.store.CreateUser(ctx, id, email, hash)
	if err != nil {
		return nil, err // 含 ErrEmailTaken，由 handler 映射
	}

	// 事务已提交，CR 创建为尽力而为；失败由修复循环补建，不阻塞注册成功。
	phase := "Pending"
	if err := s.env.Ensure(ctx, user.ID.String()); err != nil {
		s.logger.Error("ensure user environment after register failed", "reason", err, "user_id", user.ID.String())
	}
	return &RegisterResult{User: user, EnvironmentPhase: phase}, nil
}

// Login 校验凭据、必要时渐进 rehash，并签发 access token 与新 refresh token family。
func (s *Service) Login(ctx context.Context, email, password string) (*TokenResult, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return nil, ErrInvalidCredentials
	}
	user, err := s.store.GetUserByEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_, _ = VerifyPassword(password, s.dummyHash) // 恒定时间兜底
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if user.DisabledAt != nil {
		return nil, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		return nil, ErrInvalidCredentials
	}
	if NeedsRehash(user.PasswordHash, s.argon) {
		if newHash, herr := HashPassword(password, s.argon); herr == nil {
			if uerr := s.store.UpdatePasswordHash(ctx, user.ID, newHash); uerr != nil {
				s.logger.Error("progressive rehash failed", "reason", uerr, "user_id", user.ID.String())
			}
		}
	}
	return s.issueTokens(ctx, user, uuid.Nil)
}

// Refresh 轮换 refresh token：检测重用并撤销 family，过期则要求重新登录。
func (s *Service) Refresh(ctx context.Context, rawToken string) (*TokenResult, error) {
	if rawToken == "" {
		return nil, ErrInvalidCredentials
	}
	rec, err := s.store.GetRefreshTokenByHash(ctx, HashRefreshToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup refresh token: %w", err)
	}
	// 已撤销的 token 再次出现即重用攻击：撤销整个 family。
	if rec.RevokedAt != nil {
		if rerr := s.store.RevokeRefreshTokenFamily(ctx, rec.FamilyID); rerr != nil {
			s.logger.Error("revoke family on reuse failed", "reason", rerr)
		}
		return nil, ErrInvalidCredentials
	}
	if !s.now().Before(rec.ExpiresAt) {
		return nil, ErrRefreshExpired
	}
	user, err := s.store.GetUserByID(ctx, rec.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}

	result, err := s.issueTokensRotating(ctx, user, rec)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Logout 撤销当前 refresh token 所属 family；对无效 token 亦幂等返回成功。
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	rec, err := s.store.GetRefreshTokenByHash(ctx, HashRefreshToken(rawToken))
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil
		}
		return fmt.Errorf("lookup refresh token: %w", err)
	}
	if err := s.store.RevokeRefreshTokenFamily(ctx, rec.FamilyID); err != nil {
		return fmt.Errorf("revoke family: %w", err)
	}
	return nil
}

// MeResult 是当前用户与其环境状态。
type MeResult struct {
	User             User
	EnvironmentPhase string
}

// Me 返回当前用户及其 UserEnvironment 阶段。
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (*MeResult, error) {
	user, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	phase, err := s.env.Phase(ctx, user.ID.String())
	if err != nil {
		s.logger.Error("read environment phase failed", "reason", err, "user_id", user.ID.String())
		phase = "Pending"
	}
	return &MeResult{User: user, EnvironmentPhase: phase}, nil
}

func (s *Service) issueTokens(ctx context.Context, user User, _ uuid.UUID) (*TokenResult, error) {
	familyID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate family id: %w", err)
	}
	return s.mintAndStore(ctx, user, familyID, nil)
}

func (s *Service) issueTokensRotating(ctx context.Context, user User, old RefreshTokenRecord) (*TokenResult, error) {
	return s.mintAndStore(ctx, user, old.FamilyID, &old)
}

// mintAndStore 生成新 refresh token 与 access token；提供 old 时执行原子轮换，否则直接插入新 token。
func (s *Service) mintAndStore(ctx context.Context, user User, familyID uuid.UUID, old *RefreshTokenRecord) (*TokenResult, error) {
	now := s.now()
	rawRefresh, refreshHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	tokenID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate refresh token id: %w", err)
	}
	refreshExpires := now.Add(s.refreshTTL)
	replacement := RefreshTokenRecord{
		ID:        tokenID,
		UserID:    user.ID,
		FamilyID:  familyID,
		TokenHash: refreshHash,
		ExpiresAt: refreshExpires,
	}

	if old != nil {
		won, err := s.store.RotateRefreshToken(ctx, old.ID, replacement)
		if err != nil {
			return nil, fmt.Errorf("rotate refresh token: %w", err)
		}
		if !won {
			// 并发轮换只有一个胜出，落败者按重用处理并撤销 family。
			if rerr := s.store.RevokeRefreshTokenFamily(ctx, familyID); rerr != nil {
				s.logger.Error("revoke family on lost rotation failed", "reason", rerr)
			}
			return nil, ErrInvalidCredentials
		}
	} else if err := s.store.InsertRefreshToken(ctx, replacement); err != nil {
		return nil, fmt.Errorf("insert refresh token: %w", err)
	}

	access, err := s.issuer.Sign(user.ID.String(), now)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}
	return &TokenResult{
		User:             user,
		AccessToken:      access,
		AccessExpiresAt:  now.Add(s.issuer.ttl),
		RefreshToken:     rawRefresh,
		RefreshExpiresAt: refreshExpires,
	}, nil
}

func normalizeEmail(email string) (string, error) {
	trimmed := strings.TrimSpace(email)
	if len(trimmed) < 3 || len(trimmed) > 254 {
		return "", fmt.Errorf("%w: email length", ErrValidation)
	}
	at := strings.LastIndex(trimmed, "@")
	if at <= 0 || at == len(trimmed)-1 || strings.Contains(trimmed, " ") {
		return "", fmt.Errorf("%w: email format", ErrValidation)
	}
	return strings.ToLower(trimmed), nil
}

func validatePassword(password string) error {
	if len(password) < 8 || len(password) > 200 {
		return fmt.Errorf("%w: password length must be 8-200", ErrValidation)
	}
	return nil
}
