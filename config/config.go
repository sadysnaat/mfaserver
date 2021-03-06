package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	vaultAPI "github.com/hashicorp/vault/api"
	"github.com/jcmturner/mfaserver/vault"
	"github.com/jcmturner/restclient"
	"github.com/mavricknz/ldap"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

var validLogLevels = []string{"ERROR", "WARNING", "INFO", "DEBUG"}

type Config struct {
	Vault     VaultConf `json:"Vault"`
	MFAServer MFAServer `json:"MFAServer"`
	LDAP      LDAPConf  `json:"LDAP"`
}

type VaultConf struct {
	VaultReSTClientConfig *restclient.Config `json:"VaultConnection"`
	AppIDRead             *string            `json:"AppIDRead"`
	AppIDWrite            *string            `json:"AppIDWrite"`
	UserIDFile            *string            `json:"UserIDFile"`
	UserID                *string            `json:"UserID"`
	MFASecretsPath        *string            `json:"MFASecretsPath"`
	VaultConfig           *vaultAPI.Config
	VaultClient           *vaultAPI.Client
	VaultLogin            *vault.Login
}

type LDAPConf struct {
	EndPoint            *string `json:"EndPoint"`
	TrustCACert         *string `json:"TrustCACert"`
	UserDN              *string `json:"UserDN"`
	AdminGroupDN        *string `json:"AdminGroupDN"`
	AdminMembershipAttr *string `json:"AdminGroupMembershipAttribute"`
	AdminMemberUserDN   *string `json:"AdminGroupMemberDNFormat"`
	LDAPConnection      *ldap.LDAPConnection
}

type UserIdFile struct {
	UserID string `json:"UserID"`
}

type MFAServer struct {
	ListenerSocket *string `json:"ListenerSocket"`
	TLS            TLS     `json:"TLS"`
	LogFilePath    *string `json:"LogFile"`
	LogLevel       *string `json:"LogLevel"`
	Loggers        *Loggers
}

type TLS struct {
	Enabled         bool    `json:"Enabled"`
	CertificateFile *string `json:"CertificateFile"`
	KeyFile         *string `json:"KeyFile"`
}

type Loggers struct {
	Debug   *log.Logger
	Info    *log.Logger
	Warning *log.Logger
	Error   *log.Logger
}

func NewConfig() *Config {
	defSecPath := "secret/mfa"
	defSocket := "0.0.0.0:8443"
	dl := log.New(ioutil.Discard, "", os.O_APPEND)
	return &Config{
		Vault: VaultConf{
			VaultReSTClientConfig: restclient.NewConfig(),
			VaultConfig:           vaultAPI.DefaultConfig(),
			MFASecretsPath:        &defSecPath,
		},
		MFAServer: MFAServer{
			ListenerSocket: &defSocket,
			Loggers: &Loggers{
				Debug:   dl,
				Info:    dl,
				Warning: dl,
				Error:   dl,
			},
		},
	}
}

func loggerSetUp(c *Config) error {
	var logfile io.Writer
	if c.MFAServer.LogFilePath != nil {
		var err error
		logfile, err = os.OpenFile(*c.MFAServer.LogFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0664)
		if err != nil {
			return err
		}
	} else {
		logfile = os.Stdout
	}
	c.MFAServer.Loggers.Error = log.New(logfile, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
	if c.MFAServer.LogLevel != nil && isValidLogLevel(*c.MFAServer.LogLevel) {
		switch *c.MFAServer.LogLevel {
		case "DEBUG":
			c.MFAServer.Loggers.Debug = log.New(logfile, "DEBUG: ", log.Ldate|log.Ltime|log.Lshortfile)
			c.MFAServer.Loggers.Info = log.New(logfile, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
			c.MFAServer.Loggers.Warning = log.New(logfile, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
		case "INFO":
			c.MFAServer.Loggers.Info = log.New(logfile, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
			c.MFAServer.Loggers.Warning = log.New(logfile, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
		case "WARNING":
			c.MFAServer.Loggers.Warning = log.New(logfile, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
		}
		return nil
	} else {
		return errors.New(fmt.Sprintf("An invalid log level was provided. Accepted values are %v", validLogLevels))
	}
}

func Load(cfgPath string) (*Config, error) {
	j, err := ioutil.ReadFile(cfgPath)
	if err != nil {
		return nil, errors.New("Configuration file could not be openned: " + cfgPath + " " + err.Error())
	}

	c := NewConfig()
	err = json.Unmarshal(j, c)
	if err != nil {
		return nil, errors.New("Configuration file could not be parsed: " + err.Error())
	}
	err = loggerSetUp(c)
	if err != nil {
		return nil, errors.New("Configuration failed in setting up logging: " + err.Error())
	}
	c.Vault.VaultConfig.Address = *c.Vault.VaultReSTClientConfig.EndPoint
	if c.Vault.VaultReSTClientConfig.TrustCACert != nil {
		c.WithVaultCAFilePath(*c.Vault.VaultReSTClientConfig.TrustCACert)
	}
	if c.Vault.UserID == nil {
		if c.Vault.UserIDFile == nil {
			return nil, errors.New("Configuration file does not define a UserId or UserIdFile to use to access Vault")
		} else {
			_, err := c.WithVaultUserIdFile(*c.Vault.UserIDFile)
			if err != nil {
				return nil, errors.New("Configuration issue with processing the UserIDFile: " + err.Error())
			}
		}
	}
	if c.MFAServer.TLS.Enabled {
		_, err = c.WithMFATLS(*c.MFAServer.TLS.CertificateFile, *c.MFAServer.TLS.KeyFile)
		if err != nil {
			return nil, errors.New("TLS configuration for MFA Server not valid: " + err.Error())
		}
	}
	err = c.createLDAPConnection()
	if err != nil {
		return nil, errors.New("Error configuring LDAP connection: " + err.Error())
	}
	return c, nil
}

func (c *Config) WithVaultUserId(u string) *Config {
	c.Vault.UserID = &u
	return c
}

func (c *Config) WithVaultUserIdFile(u string) (*Config, error) {
	j, err := ioutil.ReadFile(u)
	if err != nil {
		return c, errors.New("Could not open UserId file at " + u + " " + err.Error())
	}
	var uf UserIdFile
	err = json.Unmarshal(j, &uf)
	if err != nil {
		return c, errors.New("UserId file could not be parsed: " + err.Error())
	}
	c.Vault.UserIDFile = &u
	c.Vault.UserID = &uf.UserID
	return c, nil
}

func (c *Config) WithVaultAppIdRead(a string) *Config {
	c.Vault.AppIDRead = &a
	return c
}
func (c *Config) WithVaultAppIdWrite(a string) *Config {
	c.Vault.AppIDWrite = &a
	return c
}

func (c *Config) WithVaultEndPoint(e string) *Config {
	c.Vault.VaultReSTClientConfig.WithEndPoint(e)
	c.Vault.VaultConfig.Address = e
	return c
}

func (c *Config) WithVaultMFASecretsPath(p string) *Config {
	c.Vault.MFASecretsPath = &p
	return c
}

func (c *Config) WithVaultConfig(cfg *vaultAPI.Config) *Config {
	c.Vault.VaultConfig = cfg
	return c
}

func (c *Config) WithVaultCACert(cert *x509.Certificate) *Config {
	if len(cert.Raw) == 0 {
		panic("Certifcate provided is empty")
	}
	tlsConfig := &tls.Config{RootCAs: x509.NewCertPool()}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	if c.Vault.VaultConfig == nil {
		c.Vault.VaultConfig = vaultAPI.DefaultConfig()
	}
	c.Vault.VaultConfig.HttpClient.Transport = transport
	tlsConfig.RootCAs.AddCert(cert)
	c.Vault.VaultReSTClientConfig.WithCACert(cert)
	return c
}

func (c *Config) WithVaultCAFilePath(caFilePath string) *Config {
	c.Vault.VaultReSTClientConfig.WithCAFilePath(caFilePath)
	tlsConfig := &tls.Config{RootCAs: x509.NewCertPool()}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	if c.Vault.VaultConfig == nil {
		c.Vault.VaultConfig = vaultAPI.DefaultConfig()
	}
	c.Vault.VaultConfig.HttpClient.Transport = transport
	// Load our trusted certificate path
	pemData, err := ioutil.ReadFile(caFilePath)
	if err != nil {
		panic(err)
	}
	ok := tlsConfig.RootCAs.AppendCertsFromPEM(pemData)
	if !ok {
		panic("Couldn't load PEM data")
	}

	return c
}

func (c *Config) WithMFAListenerSocket(s string) (*Config, error) {
	if _, err := net.ResolveTCPAddr("tcp", s); err != nil {
		return c, errors.New("Invalid listener socket defined for MFA server")
	}
	c.MFAServer.ListenerSocket = &s
	return c, nil
}

func (c *Config) WithMFATLS(certPath, keyPath string) (*Config, error) {
	if err := isValidPEMFile(certPath); err != nil {
		return c, errors.New("MFA Server TLS certificate not valid: " + err.Error())
	}
	if err := isValidPEMFile(keyPath); err != nil {
		return c, errors.New("MFA Server TLS key not valid: " + err.Error())
	}
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		cert, _ := ioutil.ReadFile(certPath)
		key, _ := ioutil.ReadFile(keyPath)
		fmt.Printf("Cert: \n %s\n Key: \n %s", cert, key)
		return c, errors.New("Key pair provided not valid: " + err.Error())
	}
	c.MFAServer.TLS = TLS{
		Enabled:         true,
		CertificateFile: &certPath,
		KeyFile:         &keyPath,
	}
	return c, nil
}

func (c *Config) WithLogLevel(l string) (*Config, error) {
	if isValidLogLevel(l) {
		c.MFAServer.LogLevel = &l
		err := loggerSetUp(c)
		if err != nil {
			return c, errors.New(fmt.Sprintf("Configuring loggers failed: %v", err))
		}
		return c, nil
	}
	return c, errors.New(fmt.Sprintf("An invalid log level of %s was provided. Accepted values are %v", l, validLogLevels))
}

func isValidPEMFile(p string) error {
	pemData, err := ioutil.ReadFile(p)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(pemData)
	if len(rest) > 0 || block.Type == "" {
		return errors.New(fmt.Sprintf("Not valid PEM format: Rest: %v Type: %v", len(rest), block.Type))
	}
	return nil
}

func isValidLogLevel(l string) bool {
	return stringInSlice(l, validLogLevels)
}

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

func (c *Config) WithLDAPConnection(e, ca, dn string) {
	c.LDAP.EndPoint = &e
	c.LDAP.TrustCACert = &ca
	c.LDAP.UserDN = &dn
	c.createLDAPConnection()
}

func (c *Config) WithLDAPAdminSettings(gdn, attr, m string) {
	c.LDAP.AdminGroupDN = &gdn
	c.LDAP.AdminMembershipAttr = &attr
	//TODO check that the following includes "{username}"
	c.LDAP.AdminMemberUserDN = &m
}

func (c *Config) createLDAPConnection() error {
	var port uint64
	s := *c.LDAP.EndPoint
	if strings.HasPrefix(*c.LDAP.EndPoint, "ldaps://") {
		s = s[len("ldaps://"):]
		if i := strings.LastIndex(s, ":"); i != -1 {
			port, _ = strconv.ParseUint(s[i+1:], 10, 16)
			s = s[0:i]
		} else {
			port = 636
		}

		tlsConfig := &tls.Config{RootCAs: x509.NewCertPool()}
		pemData, err := ioutil.ReadFile(*c.LDAP.TrustCACert)
		if err != nil {
			return err
		}
		ok := tlsConfig.RootCAs.AppendCertsFromPEM(pemData)
		if !ok {
			return errors.New("Couldn't load PEM data for LDAP connection")
		}

		c.LDAP.LDAPConnection = ldap.NewLDAPTLSConnection(s, uint16(port), tlsConfig)
	} else if strings.HasPrefix(*c.LDAP.EndPoint, "ldap://") {
		s = s[len("ldap://"):]
		if i := strings.LastIndex(s, ":"); i != -1 {
			port, _ = strconv.ParseUint(s[i+1:], 10, 16)
			s = s[0:i]
		} else {
			port = 389
		}
		c.LDAP.LDAPConnection = ldap.NewLDAPConnection(s, uint16(port))
	} else {
		return errors.New("Invalid protocol in LDAP endpoint: " + *c.LDAP.EndPoint)
	}
	return nil
}
