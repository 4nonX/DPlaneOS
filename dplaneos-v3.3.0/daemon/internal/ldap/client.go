package ldap

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
)

// Config holds LDAP configuration
type Config struct {
	Enabled            bool     `json:"enabled"`
	Server             string   `json:"server"`
	Port               int      `json:"port"`
	UseTLS             bool     `json:"use_tls"`
	BindDN             string   `json:"bind_dn"`
	BindPassword       string   `json:"bind_password"`
	BaseDN             string   `json:"base_dn"`
	UserFilter         string   `json:"user_filter"`
	UserIDAttribute    string   `json:"user_id_attribute"`
	UserNameAttribute  string   `json:"user_name_attribute"`
	UserEmailAttribute string   `json:"user_email_attribute"`
	GroupBaseDN        string   `json:"group_base_dn"`
	GroupFilter        string   `json:"group_filter"`
	GroupMemberAttr    string   `json:"group_member_attribute"`
	GroupMappings      []GroupMapping `json:"group_mappings"`
	JITProvisioning    bool     `json:"jit_provisioning"`
	DefaultRole        string   `json:"default_role"`
	Timeout            int      `json:"timeout"` // seconds
}

// GroupMapping maps LDAP groups to D-PlaneOS roles
type GroupMapping struct {
	LDAPGroup string `json:"ldap_group"`
	RoleID    int    `json:"role_id"`
	RoleName  string `json:"role_name"`
}

// User represents an LDAP user
type User struct {
	DN         string
	Username   string
	Email      string
	FullName   string
	Groups     []string
	Attributes map[string][]string
}

// Client wraps LDAP connection
type Client struct {
	config *Config
	conn   *ldap.Conn
}

// NewClient creates a new LDAP client
func NewClient(config *Config) (*Client, error) {
	return &Client{
		config: config,
	}, nil
}

// Connect establishes connection to LDAP server
func (c *Client) Connect() error {
	address := fmt.Sprintf("%s:%d", c.config.Server, c.config.Port)
	
	var conn *ldap.Conn
	var err error
	
	if c.config.UseTLS {
		tlsConfig := &tls.Config{
			ServerName: c.config.Server,
			MinVersion: tls.VersionTLS12,
		}
		conn, err = ldap.DialTLS("tcp", address, tlsConfig)
	} else {
		conn, err = ldap.Dial("tcp", address)
	}
	
	if err != nil {
		return fmt.Errorf("failed to connect to LDAP server: %w", err)
	}
	
	// Set timeout
	if c.config.Timeout > 0 {
		conn.SetTimeout(time.Duration(c.config.Timeout) * time.Second)
	}
	
	c.conn = conn
	return nil
}

// Close closes the LDAP connection
func (c *Client) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// Bind performs LDAP bind with service account
func (c *Client) Bind() error {
	if c.conn == nil {
		return fmt.Errorf("not connected to LDAP server")
	}
	
	err := c.conn.Bind(c.config.BindDN, c.config.BindPassword)
	if err != nil {
		return fmt.Errorf("bind failed: %w", err)
	}
	
	return nil
}

// Authenticate authenticates a user against LDAP
func (c *Client) Authenticate(username, password string) (*User, error) {
	// Connect and bind with service account
	if err := c.Connect(); err != nil {
		return nil, err
	}
	defer c.Close()
	
	if err := c.Bind(); err != nil {
		return nil, err
	}
	
	// Search for user
	user, err := c.searchUser(username)
	if err != nil {
		return nil, err
	}
	
	// Attempt to bind as the user to verify password
	err = c.conn.Bind(user.DN, password)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: invalid credentials")
	}
	
	// Re-bind as service account to fetch groups
	if err := c.Bind(); err != nil {
		return nil, err
	}
	
	// Fetch user groups
	groups, err := c.getUserGroups(user.DN)
	if err != nil {
		return nil, err
	}
	user.Groups = groups
	
	return user, nil
}

// searchUser searches for a user by username
func (c *Client) searchUser(username string) (*User, error) {
	filter := strings.ReplaceAll(c.config.UserFilter, "{username}", username)
	
	searchRequest := ldap.NewSearchRequest(
		c.config.BaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		[]string{
			c.config.UserIDAttribute,
			c.config.UserNameAttribute,
			c.config.UserEmailAttribute,
			"cn",
			"displayName",
			"memberOf",
		},
		nil,
	)
	
	result, err := c.conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("user search failed: %w", err)
	}
	
	if len(result.Entries) == 0 {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	
	if len(result.Entries) > 1 {
		return nil, fmt.Errorf("multiple users found for: %s", username)
	}
	
	entry := result.Entries[0]
	
	user := &User{
		DN:         entry.DN,
		Username:   entry.GetAttributeValue(c.config.UserIDAttribute),
		Email:      entry.GetAttributeValue(c.config.UserEmailAttribute),
		FullName:   entry.GetAttributeValue("displayName"),
		Attributes: make(map[string][]string),
	}
	
	// If no display name, use cn
	if user.FullName == "" {
		user.FullName = entry.GetAttributeValue("cn")
	}
	
	// If no username from attribute, use what was searched
	if user.Username == "" {
		user.Username = username
	}
	
	return user, nil
}

// getUserGroups retrieves all groups a user is member of
func (c *Client) getUserGroups(userDN string) ([]string, error) {
	if c.config.GroupBaseDN == "" {
		return []string{}, nil
	}
	
	filter := strings.ReplaceAll(c.config.GroupFilter, "{user_dn}", userDN)
	
	searchRequest := ldap.NewSearchRequest(
		c.config.GroupBaseDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		[]string{"cn", "distinguishedName"},
		nil,
	)
	
	result, err := c.conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("group search failed: %w", err)
	}
	
	var groups []string
	for _, entry := range result.Entries {
		groupName := entry.GetAttributeValue("cn")
		if groupName != "" {
			groups = append(groups, groupName)
		}
	}
	
	return groups, nil
}

// TestConnection tests LDAP connection and bind
func (c *Client) TestConnection() error {
	if err := c.Connect(); err != nil {
		return err
	}
	defer c.Close()
	
	if err := c.Bind(); err != nil {
		return err
	}
	
	return nil
}

// MapGroupsToRoles maps LDAP groups to D-PlaneOS roles
func (c *Client) MapGroupsToRoles(groups []string) []int {
	var roleIDs []int
	roleMap := make(map[int]bool) // Deduplicate
	
	for _, group := range groups {
		for _, mapping := range c.config.GroupMappings {
			if strings.EqualFold(group, mapping.LDAPGroup) {
				if !roleMap[mapping.RoleID] {
					roleIDs = append(roleIDs, mapping.RoleID)
					roleMap[mapping.RoleID] = true
				}
			}
		}
	}
	
	return roleIDs
}

// GetDefaultConfig returns default LDAP configuration
func GetDefaultConfig() *Config {
	return &Config{
		Enabled:            false,
		Server:             "ldap.example.com",
		Port:               389,
		UseTLS:             true,
		BindDN:             "cn=service-account,dc=example,dc=com",
		BindPassword:       "",
		BaseDN:             "dc=example,dc=com",
		UserFilter:         "(&(objectClass=user)(sAMAccountName={username}))",
		UserIDAttribute:    "sAMAccountName",
		UserNameAttribute:  "displayName",
		UserEmailAttribute: "mail",
		GroupBaseDN:        "dc=example,dc=com",
		GroupFilter:        "(&(objectClass=group)(member={user_dn}))",
		GroupMemberAttr:    "member",
		GroupMappings:      []GroupMapping{},
		JITProvisioning:    true,
		DefaultRole:        "user",
		Timeout:            10,
	}
}

// ValidateConfig validates LDAP configuration
func ValidateConfig(config *Config) error {
	if !config.Enabled {
		return nil
	}
	
	if config.Server == "" {
		return fmt.Errorf("LDAP server is required")
	}
	
	if config.Port <= 0 || config.Port > 65535 {
		return fmt.Errorf("invalid port number")
	}
	
	if config.BindDN == "" {
		return fmt.Errorf("bind DN is required")
	}
	
	if config.BindPassword == "" {
		return fmt.Errorf("bind password is required")
	}
	
	if config.BaseDN == "" {
		return fmt.Errorf("base DN is required")
	}
	
	if config.UserFilter == "" {
		return fmt.Errorf("user filter is required")
	}
	
	if !strings.Contains(config.UserFilter, "{username}") {
		return fmt.Errorf("user filter must contain {username} placeholder")
	}
	
	return nil
}
