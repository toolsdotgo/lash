package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	typ "github.com/aws/aws-sdk-go-v2/service/sso/types"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/pkg/browser"
)

var version = "edge"

type config struct {
	basedir string

	Region          string            `json:"region"`
	StartURL        string            `json:"start_url"`
	RoleStripPrefix string            `json:"role_strip_prefix"`
	RoleStripSuffix string            `json:"role_strip_suffix"`
	StripPrefix     string            `json:"strip_prefix"`
	StripSuffix     string            `json:"strip_suffix"`
	Nicks           map[string]string `json:"nicks"`
}

type token struct {
	path string

	Value     string
	ExpiresIn int
}

type account struct {
	Name  string
	Slug  string
	ID    string
	Roles []string
}

type profile struct {
	path   string
	token  string
	region string
	badges map[string]badge

	Accounts []account
}

type badge struct {
	id   string // account id
	role string // role name
}

type sessionToken struct {
	SessionID    string `json:"sessionId"`
	SessionKey   string `json:"sessionKey"`
	SessionToken string `json:"sessionToken"`
}

type federationResponse struct {
	SigninToken string
}

func main() {
	homedir, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get home directory: %v\n", err)
		os.Exit(1)
	}

	// flags
	fbasedir := flag.String("d", filepath.Join(homedir, ".aws"), "the directory with the credentials file and lash/ subdir")
	fhelp := flag.Bool("h", false, "show help")
	finit := flag.Bool("init", false, "make the lash sub-directory and re-create the config.json file")
	fnonick := flag.Bool("n", false, "disable nicknames")
	frefresh := flag.Bool("r", false, "refresh caches (token and profiles)")
	furl := flag.Bool("u", false, "generate an aws console url for the chosen role")
	fver := flag.Bool("v", false, "print program version")
	flag.Parse()

	if *fhelp {
		fmt.Print(usageTop)
		os.Exit(64)
	}

	if *fver {
		fmt.Println(version)
		os.Exit(0)
	}

	choice := flag.Arg(0)
	cmdname := flag.Arg(1)
	cmd := ""
	if flag.NArg() > 1 {
		clean := filepath.Clean(cmdname)
		if strings.HasPrefix(cmdname, "./") || strings.HasPrefix(cmdname, "/") {
			clean, err = filepath.Abs(cmdname)
			if err != nil {
				fmt.Fprintf(os.Stderr, "command '%s' not parseable: %v\n", cmdname, err)
				os.Exit(9)
			}
		}
		cmd, err = exec.LookPath(clean)
		if err != nil {
			fmt.Fprintf(os.Stderr, "command '%s' not found: %v\n", cmdname, err)
			os.Exit(9)
		}
	}

	if *finit {
		if err := setup(*fbasedir); err != nil {
			fmt.Fprintf(os.Stderr, "cant create config: %v\n", err)
			os.Exit(3)
		}
	}

	cfg, err := loadConfig(*fbasedir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant load config: %v\n", err)
		os.Exit(2)
	}

	p, err := getProfile(cfg, *frefresh)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get profile: %v\n", err)
		os.Exit(4)
	}

	p.badges = map[string]badge{}
	for _, a := range p.Accounts {
		for _, r := range a.Roles {
			role := strings.TrimPrefix(r, cfg.RoleStripPrefix)
			role = strings.TrimSuffix(a.Slug+"-"+role, cfg.RoleStripSuffix)
			p.badges[role] = badge{id: a.ID, role: r}
		}
	}

	fromnick := false
	if !*fnonick {
		if _, ok := cfg.Nicks[choice]; ok {
			choice = cfg.Nicks[choice]
			fromnick = true
		}
	}

	if _, ok := p.badges[choice]; !ok { // there's no full-match for this choice
		matches := []string{}
		roles := []string{}
		for role := range p.badges {
			roles = append(roles, role)
			if strings.Contains(role, choice) {
				matches = append(matches, role)
			}
		}
		if len(matches) == 1 {
			choice = matches[0]
			goto fuzzy // 1985 coming at you hard
		}
		sort.Strings(roles)
		msg := "available roles:"
		if choice == "" {
			msg = "use one of the following roles:"
		}
		fmt.Fprintln(os.Stderr, msg)
		for _, role := range roles {
			line := "      " + role
			if choice != "" && in(matches, role) {
				line = cGreen + "  ~>  " + role + cReset
			}
			fmt.Println(line)
		}

		if choice == "" {
			os.Exit(0)
		}
		if len(matches) > 1 {
			fmt.Fprintf(os.Stderr, "'%s' matches more than one profile\n", choice)
			os.Exit(11)
		}
		fmt.Fprintf(os.Stderr, "'%s' does not match any profile\n", choice)
		os.Exit(11)
	}
fuzzy:

	selmsg := "selected: "
	if fromnick {
		selmsg = "selected (via nicks): "
	}
	fmt.Fprintln(os.Stderr, selmsg+choice)

	keys, err := p.getKeys(choice)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get keys for %s: %v\n", choice, err)
		os.Exit(5)
	}

	if *furl {
		fedUrl := "https://signin.aws.amazon.com/federation"
		t := sessionToken{
			SessionID:    keys["AccessKeyId"],
			SessionKey:   keys["SecretAccessKey"],
			SessionToken: keys["SessionToken"],
		}
		b, err := json.Marshal(t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant marshal keys: %v\n", err)
			os.Exit(12)
		}

		req, err := http.NewRequest("GET", fedUrl, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant create federation request: %v\n", err)
			os.Exit(12)
		}

		qf := req.URL.Query()
		qf.Add("Action", "getSigninToken")
		qf.Add("SessionType", "json")
		qf.Add("Session", string(b))
		req.URL.RawQuery = qf.Encode()
		client := &http.Client{}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to get federation response: %v\n", err)
			os.Exit(12)
		}
		defer func() {
			err := resp.Body.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to close federation response body: %v\n", err)
			}
		}()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant read federation response body: %v\n", err)
			os.Exit(12)
		}
		var fedResp federationResponse
		err = json.Unmarshal(body, &fedResp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant unmarshal federation response body: %v\n", err)
			os.Exit(12)
		}
		signinToken := fedResp.SigninToken

		u, err := url.Parse(fedUrl)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cant parse sign in url: %v\n", err)
			os.Exit(12)
		}
		qs := u.Query()
		qs.Set("SigninToken", signinToken)
		qs.Set("Destination", "https://console.aws.amazon.com/")
		qs.Set("Action", "login")
		u.RawQuery = qs.Encode()
		fmt.Println(u.String())
		os.Exit(0)
	}

	// write the credentials file and exit zero
	if cmd == "" {
		if err := writeCreds(cfg, keys); err != nil {
			fmt.Fprintf(os.Stderr, "cant write creds file: %v\n", err)
			os.Exit(6)
		}
		os.Exit(0)
	}

	// add the creds to our current environ
	_ = os.Setenv("AWS_ACCESS_KEY_ID", keys["AccessKeyId"])
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", keys["SecretAccessKey"])
	_ = os.Setenv("AWS_SESSION_TOKEN", keys["SessionToken"])
	_ = os.Setenv("AWS_SESSION_EXPIRATION", keys["Expiration"])
	_ = os.Setenv("AWS_PROFILE_NAME", choice)

	/* #nosec */
	if err := syscall.Exec(cmd, flag.Args()[1:], os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "cant exec command %s: %v\n", cmd, err)
		os.Exit(9)
	}
	os.Exit(0)
}

func getProfile(cfg config, refresh bool) (profile, error) {
	p := profile{
		path:   filepath.Join(cfg.basedir, "lash", "profile.json"),
		region: cfg.Region,
	}

	// get the oidc token and write the cache if a new one is generated
	t := token{path: filepath.Join(cfg.basedir, "lash", "oidc.json")}
	if refresh {
		_ = os.Remove(t.path)
	}
	if err := t.getCache(); err != nil {
		return p, fmt.Errorf("cant get oidc token: %w", err)
	}
	if t.Value == "" { // no token cache or expired
		err := t.create(cfg)
		if err != nil {
			return p, fmt.Errorf("cant create token cache file %s: %w", t.path, err)
		}
	}
	if t.Value == "" { // backstop
		return p, errors.New("cant get oidc token, no reason, just cant")
	}

	// get the roles for each account and store them in profile.Accounts
	p.token = t.Value
	if refresh {
		_ = os.Remove(p.path)
	}
	if err := p.getCache(); err != nil {
		return p, fmt.Errorf("cant get profile cache %s: %w", p.path, err)
	}
	if len(p.Accounts) < 1 {
		err := p.create(cfg)
		if err != nil {
			return p, fmt.Errorf("cant get accounts or roles: %w", err)
		}
	}

	return p, nil
}

func writeCreds(cfg config, keys map[string]string) error {
	cfp := filepath.Join(cfg.basedir, "credentials")

	cf, err := os.OpenFile(filepath.Clean(cfp), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cant open creds file %s: %w", cfp, err)
	}

	w := func(pos string) {
		// NOTE this function doesn't handle errors
		b, _ := os.ReadFile(filepath.Clean(cfp + "-" + pos))
		if len(b) < 1 {
			return
		}
		_, _ = cf.WriteString(string(b)) // closure
	}

	tmpl, err := template.New("lash").Parse(credsTmpl)
	if err != nil {
		return fmt.Errorf("cant parse creds template (internal): %w", err)
	}

	w("head")
	err = tmpl.Execute(cf, keys)
	w("tail")
	if err != nil {
		return fmt.Errorf("cant write creds file (exec template): %w", err)
	}

	return cf.Close()
}

func (p *profile) getCache() error {
	fi, b, err := getFile(filepath.Clean(p.path))
	if err != nil {
		return fmt.Errorf("cant get profile cache file %s: %w", p.path, err)
	}
	if fi == nil || b == nil {
		return nil
	}

	if err := json.Unmarshal(b, &p); err != nil {
		return fmt.Errorf("cant unmarshal profile cache file %s: %w", p.path, err)
	}

	return nil
}

func (p *profile) create(cfg config) error {
	if p.token == "" {
		return errors.New("invalid token")
	}

	retrycfg, err := awscfg.LoadDefaultConfig(
		context.TODO(),
		awscfg.WithRegion(p.region),
		awscfg.WithRetryer(func() aws.Retryer {
			retryer := retry.AddWithMaxAttempts(retry.NewStandard(), 10)
			return retry.AddWithMaxBackoffDelay(retryer, 30*time.Second)
		}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cant get aws config: %v\n", err)
		os.Exit(1)
	}

	cli := sso.NewFromConfig(retrycfg)
	pg := sso.NewListAccountsPaginator(
		cli,
		&sso.ListAccountsInput{AccessToken: aws.String(p.token)},
	)

	accts := make(chan account)
	var wg sync.WaitGroup

	for pg.HasMorePages() {
		o, err := pg.NextPage(context.Background())
		if err != nil {
			return fmt.Errorf("cant list accounts: %w", err)
		}
		for _, a := range o.AccountList {
			if a.AccountName == nil {
				fmt.Fprintln(os.Stderr, "nil account name, skipping")
				continue
			}
			if a.AccountId == nil {
				fmt.Fprintln(os.Stderr, "nil account id, skipping")
				continue
			}

			wg.Add(1)
			go func(a typ.AccountInfo) {
				acct := account{Name: *a.AccountName, ID: *a.AccountId}
				pg := sso.NewListAccountRolesPaginator(
					cli,
					&sso.ListAccountRolesInput{
						AccessToken: aws.String(p.token),
						AccountId:   a.AccountId,
					},
				)
				for pg.HasMorePages() {
					o, err := pg.NextPage(context.Background())
					if err != nil {
						fmt.Fprintf(os.Stderr, "error getting next page of roles for account id '%v', all roles may not be available: %v\n", *a.AccountId, err)
						wg.Done()
						return
					}
					for _, r := range o.RoleList {
						if r.RoleName == nil {
							fmt.Fprintln(os.Stderr, "nil role name, skipping")
							continue
						}
						acct.Roles = append(acct.Roles, *r.RoleName)
					}
				}
				accts <- acct
				wg.Done()
			}(a)
		}
	}
	go func() {
		wg.Wait()
		close(accts)
	}()

	for a := range accts {
		a.Slug = slugify(a.Name, cfg.StripPrefix, cfg.StripSuffix)
		p.Accounts = append(p.Accounts, a)
	}

	_ = os.Remove(p.path)
	b, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("cant marshal profiles: %w", err)
	}
	if err := os.WriteFile(p.path, b, 0600); err != nil {
		return fmt.Errorf("cant write profile cache %s: %w", p.path, err)
	}

	return nil
}

func (p profile) getKeys(choice string) (map[string]string, error) {
	keys := map[string]string{}
	badge, ok := p.badges[choice]
	if !ok {
		return keys, fmt.Errorf("'%s' does not match any profile", choice)
	}

	ssoc := sso.New(sso.Options{Region: p.region})
	o, err := ssoc.GetRoleCredentials(
		context.Background(),
		&sso.GetRoleCredentialsInput{
			AccessToken: aws.String(p.token),
			AccountId:   aws.String(badge.id),
			RoleName:    aws.String(badge.role),
		},
	)
	if err != nil {
		return keys, fmt.Errorf("cant get role credentials for %s: %w", choice, err)
	}

	// TODO can any of these pointers be nil?
	keys["AccessKeyId"] = *o.RoleCredentials.AccessKeyId
	keys["SecretAccessKey"] = *o.RoleCredentials.SecretAccessKey
	keys["SessionToken"] = *o.RoleCredentials.SessionToken
	keys["Expiration"] = strconv.Itoa(int(o.RoleCredentials.Expiration))

	return keys, nil
}

func (t *token) getCache() error {
	fi, b, err := getFile(filepath.Clean(t.path))
	if err != nil {
		return fmt.Errorf("cant get token cache file %s: %w", t.path, err)
	}
	if fi == nil || b == nil {
		return nil
	}

	if err := json.Unmarshal(b, &t); err != nil {
		return fmt.Errorf("cant unmarshal token cache file %s: %w", t.path, err)
	}

	etime := fi.ModTime().Add(time.Duration(t.ExpiresIn) * time.Second)
	if time.Now().Local().After(etime) {
		t.Value = ""
	}

	return nil
}

func (t *token) create(cfg config) error {
	opts := func(o *ssooidc.Options) { o.Region = cfg.Region }
	oidc := ssooidc.New(ssooidc.Options{})
	o, err := oidc.RegisterClient(
		context.Background(),
		&ssooidc.RegisterClientInput{
			ClientName: aws.String("lash"),
			ClientType: aws.String("public"),
		},
		opts,
	)
	if err != nil {
		return fmt.Errorf("cant register for oidc: %w", err)
	}

	auth, err := oidc.StartDeviceAuthorization(
		context.Background(),
		&ssooidc.StartDeviceAuthorizationInput{
			ClientId:     o.ClientId,
			ClientSecret: o.ClientSecret,
			StartUrl:     aws.String(cfg.StartURL),
		},
		opts,
	)
	if err != nil {
		return fmt.Errorf("cant start device auth: %w", err)
	}

	_ = browser.OpenURL(*auth.VerificationUriComplete)
	fmt.Println("press enter when it's cooked")
	_, err = fmt.Scanln()
	if err != nil {
		return fmt.Errorf("cant scan stdin: %w", err)
	}

	tok, err := oidc.CreateToken(
		context.Background(),
		&ssooidc.CreateTokenInput{
			ClientId:     o.ClientId,
			ClientSecret: o.ClientSecret,
			DeviceCode:   auth.DeviceCode,
			GrantType:    aws.String("urn:ietf:params:oauth:grant-type:device_code"),
		},
		opts,
	)
	if err != nil {
		return fmt.Errorf("cant create token: %w", err)
	}

	t.Value = *tok.AccessToken
	t.ExpiresIn = int(tok.ExpiresIn)

	_ = os.Remove(t.path)
	b, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("cant marshal new token: %w", err)
	}
	if err := os.WriteFile(t.path, b, 0600); err != nil {
		return fmt.Errorf("cant write token cache %s: %w", t.path, err)
	}

	return nil
}

func getFile(path string) (os.FileInfo, []byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("cant stat %s: %w", path, err)
	}
	if fi.Mode().Perm() != 0600 {
		return nil, nil, fmt.Errorf("cache file %s must have perms of 0600", path)
	}

	// NOTE there's no owner check, not sure if needed
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, nil, fmt.Errorf("cant read token cache file %s: %w", path, err)
	}

	return fi, b, nil
}

func loadConfig(basedir string) (config, error) {
	cf := filepath.Join(basedir, "lash", "config.json")
	b, err := os.ReadFile(filepath.Clean(cf))
	if err != nil {
		return config{}, fmt.Errorf("cant open config: %w\ndo you need to run `lash -init` to create your config file?", err)
	}
	c := config{basedir: basedir}
	if err := json.Unmarshal(b, &c); err != nil {
		return config{}, fmt.Errorf("cant unmarshal config: %w", err)
	}
	if c.Region == "" {
		return config{}, errors.New("config error: missing region")
	}
	if c.StartURL == "" {
		return config{}, errors.New("config error: missing start_url")
	}
	return c, nil
}

func setup(basedir string) error {
	base := filepath.Clean(basedir)
	if _, err := os.Stat(base); os.IsNotExist(err) {
		return fmt.Errorf("basedir '%s' does not exist", base)
	}

	lash := filepath.Clean(filepath.Join(base, "lash"))
	if _, err := os.Stat(lash); os.IsNotExist(err) {
		if err := os.Mkdir(lash, 0700); err != nil {
			return fmt.Errorf("cant mkdir '%s': %w", lash, err)
		}
	}

	region, starturl := "", ""
	r := bufio.NewReader(os.Stdin)
	fmt.Println("tell me some things for config")
	fmt.Println("~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~")
	for {
		fmt.Print("   region ~> ")
		region, _ = r.ReadString('\n')
		region = strings.Replace(region, "\n", "", -1)
		if region != "" {
			break
		}
	}
	for {
		fmt.Print("start url ~> ")
		starturl, _ = r.ReadString('\n')
		starturl = strings.Replace(starturl, "\n", "", -1)
		if starturl != "" {
			break
		}
	}

	cfg := config{
		basedir:  basedir,
		Region:   region,
		StartURL: starturl,
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("cant marshal new config: %w", err)
	}

	lashcfg := filepath.Clean(filepath.Join(lash, "config.json"))
	if err := os.WriteFile(lashcfg, b, 0600); err != nil {
		return fmt.Errorf("cant write config %s: %w", lashcfg, err)
	}
	return nil
}

func slugify(s, sp, ss string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.TrimPrefix(s, sp)
	s = strings.TrimSuffix(s, ss)
	return s
}

func in(ss []string, s string) bool {
	for _, v := range ss {
		if s == v {
			return true
		}
	}
	return false
}

var cReset = "\033[0m"
var cGreen = "\033[32m"

func init() {
	if runtime.GOOS == "windows" {
		cReset = ""
		cGreen = ""
	}
}

/* #nosec */
const credsTmpl = `[default]
aws_access_key_id={{ .AccessKeyId }}
aws_secret_access_key={{ .SecretAccessKey }}
aws_session_token={{ .SessionToken }}
aws_security_token={{ .SessionToken }}
`

const usageTop = `──╖  ╭─┐╭──┐ ╥──┤ less annoying sso helper
  ║  ┼─┤┴─┐┼─╫  └───╴╶─╶───────╶─────────╱╴╴╴
──╨──┘ └──┘│ ╨╴
           ╰──╮
              ╰───╶─╴╶

usage: lash [flags] [profile [command [args...]]]

SUMMARY
  lash integrates with aws sso and fully manages the aws credentials file
  use it either as an account picker, a command shim, or to get a console url

FLAGS
  -d  the directory with the creds and lash/ subdirectory (basedir)
  -h  print this help
  -n  dont use the nickname map from config
  -r  refresh the oidc token and the profiles (full refresh)
  -u  generate an aws console url for the chosen role
  -v  print the program version

  -init  initializes the lash config.json file (and lash/ subdirectory) by
         prompting for region and start url values. nullifies any other
         configuration settings (nicks, prefixes, etc).

PROFILES
  lash refers to the combination of an account and permission set as a profile.
  when lash retrieves the list of accounts and roles from aws sso, it combines
  them as "Account Name-Permission Set Name", then slugifies them as
  "account-name-permission-set-name". thus the role "admin" in the "Data Dev"
  account is rendered as "data-dev-admin".

ARGUMENTS
  profile  [optional] a string which uniquely matches a profile
  command  [optional] a command to run with creds in the environ

BASE DIRECTORY
  is probably ~/.aws and must contain the credentials file. lash will write to
  the credentials file without regard for your feelings. see CUSTOM CREDENTIALS
  below if this frightens you.

  use the -init flag to create the subdirectory and an initial config.json if
  you like.

CONFIG FILE
  is JSON - soz
  it's an object with the following top-level keys

  region             the aws region, e.g.,, ap-southeast-2
  start_url          the awsapps sso landing url
  nicks              [optional] an object with keys for role nicknames and the
                     value of the actual role
  role_strip_prefix  [optional] a string to strip from the beginning of a role
                     name. e.g. "team-name-"
  role_strip_suffix  [optional] a string to strip from the end of a role name
                     e.g. "-developer"
  strip_prefix       [optional] a string to strip from the beginning of profile
                     names. e.g., "company-slug-"
  strip_suffix       [optional] a string to strip from the end of profile names

  e.g.: {
    "region": "ap-southeast-2",
    "start_url": "https://startup.awsapps.com/start",
    "nicks": {"lab": "project-lab-poweruser"},
    "strip_prefix": "startup-"
  }

CUSTOM CREDENTIALS
  if you have named, pet creds you need to keep around, use either
  credentials-head or credentials-tail files in the same directory as
  credentials (the aws creds file). they will not be managed and will be
  added to the resulting creds file in a predictable manner.

EXIT CODES
  1   initialization error - probably something is wrong with the os env
  2   cant load config file (lash/config.json)
  3   problem creating lash subdirectory or config file
  4   problem getting or writing the cache files (oidc token and profiles)
  5   problem getting the role credentials (keys - probably an auth thing)
  6   problem managing the credentials file (permissions or existence)
  9   problem with supplied command (command shim mode)
  11  supplied profile slug has no matches or more than one match
  12  problem getting console signin url
  64  incorrect invocation (usage)
`
