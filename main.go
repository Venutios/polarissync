package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	"github.com/go-ldap/ldap/v3"
	"github.com/spf13/viper"
)

var (
	config      Configuration
	dbComputers []string
	adComputers []string
	logFile     *os.File
	errorLogger *log.Logger
	infoLogger  *log.Logger
)

func main() {
	viper.SetConfigName("config")
	viper.SetConfigType("json")
	viper.AddConfigPath(".")

	viper.SetDefault("logging.enabled", false)
	viper.SetDefault("logging.location", ".")
	viper.SetDefault("azure.enabled", false)
	viper.SetDefault("activedirectory.enabled", true)
	viper.SetDefault("activedirectory.host", "127.0.0.1")
	viper.SetDefault("database.host", "127.0.0.1")
	viper.SetDefault("database.port", 1433)
	viper.SetDefault("database.trusted", true)
	viper.SetDefault("database.exemptComputers", []string{})

	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("unable to read config file: %w", err))
	}

	err = viper.Unmarshal(&config)
	if err != nil {
		panic(fmt.Errorf("config file is corrupt: %w", err))
	}

	if config.Logging.Enabled {
		//generate a log file name based on the current date, create the file or append if it already exists
		now := time.Now()
		logfilename := "polarissync" + strconv.Itoa(now.Year()) + strconv.Itoa(int(now.Month())) + strconv.Itoa(now.Day()) + ".log"
		logFile, err = os.OpenFile(filepath.Join(config.Logging.Location, logfilename), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			panic(fmt.Errorf("failed to open log file: %w", err))
		}
		errorLogger = log.New(logFile, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
		infoLogger = log.New(logFile, "INFO: ", log.Ldate|log.Ltime)
	}

	writeInfo("Loading the list of computers from the database")
	listDBComputers()
	if config.ActiveDirectory.Enabled {
		writeInfo("Loading the list of computers from Active Directory")
		listADComputers()
	}
	if config.Azure.Enabled {
		writeInfo("Loading the list of computers from Azure")
		listAzureComputers()
	}
	writeInfo("Searching for computers to remove from the database")
	findComputersToRemoveFromDB()
}

func writeInfo(msg string) {
	if infoLogger != nil {
		infoLogger.Println(msg)
	}
}

func writeError(err error) {
	if errorLogger != nil {
		errorLogger.Panic(err)
	}
	panic(err)
}

// Build the database connection string based on the config of a trusted connection, or specifying credentials
func buildConnString() string {
	if config.Database.Trusted {
		return fmt.Sprintf("server=%s;port=%d;database=%s;trusted_connection=yes", config.Database.Host, config.Database.Port, config.Database.Name)
	} else {
		username := config.Database.Domain + "\\" + config.Database.Username
		return fmt.Sprintf("server=%s;user id=%s;password=%s;port=%d;database=%s", config.Database.Host, username, config.Database.Password, config.Database.Port, config.Database.Name)
	}
}

// Populate the dbComputers slice with a list of computers names
func listDBComputers() {
	conn, err := sql.Open("mssql", buildConnString())
	if err != nil {
		writeError(fmt.Errorf("database connection failed: %w", err))
	}
	defer conn.Close()

	rows, err := conn.Query("select ComputerName from Polaris.Workstations where ComputerName is not null")
	if err != nil {
		writeError(fmt.Errorf("failed to load workstations: %w", err))
	}
	defer rows.Close()

	for rows.Next() {
		var ComputerName string
		if err := rows.Scan(&ComputerName); err != nil {
			writeError(fmt.Errorf("error reading record from database: %w", err))
		}
		dbComputers = append(dbComputers, strings.ToUpper(ComputerName))
	}
	if err = rows.Err(); err != nil {
		writeError(fmt.Errorf("error reading from database: %w", err))
	}

	writeInfo(strconv.Itoa(len(dbComputers)) + " records retrieved")
}

// Populate the adComputers slice with a list of computers names
func listADComputers() {
	l, err := ldap.DialURL(fmt.Sprintf("ldap://%s:389", config.ActiveDirectory.Host))
	if err != nil {
		writeError(fmt.Errorf("unable to connect to AD server: %w", err))
	}
	defer l.Close()

	username := config.ActiveDirectory.Domain + "\\" + config.ActiveDirectory.Username

	if err := l.Bind(username, config.ActiveDirectory.Password); err != nil {
		writeError(fmt.Errorf("unable to bind to ldap: %w", err))
	}

	//Retrieve only the cn attribute for all computer objects
	searhReq := ldap.NewSearchRequest(config.ActiveDirectory.Dn, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false, "(&(objectClass=computer))", []string{"cn"}, nil)

	result, err := l.Search(searhReq)
	if err != nil {
		writeError(fmt.Errorf("ldap search error: %w", err))
	}

	if len(result.Entries) > 0 {
		for _, x := range result.Entries {
			adComputers = append(adComputers, strings.ToUpper(x.Attributes[0].Values[0]))
		}
	} else {
		writeError(fmt.Errorf("no results returned from ldap search"))
	}

	writeInfo(strconv.Itoa(len(adComputers)) + " records retrieved from AD")
}

// Add records for Azure joined machine to the adComputers slice
func listAzureComputers() {
	cmd := exec.Command("powershell", "-nologo", "-noprofile")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		defer stdin.Close()
		fmt.Fprintln(stdin, "$userName = '"+config.ActiveDirectory.Username+"@"+config.Azure.Domain+"'")
		fmt.Fprintln(stdin, "$passText = '"+config.ActiveDirectory.Password+"'")
		fmt.Fprintln(stdin, "$secpasswd = ConvertTo-SecureString -String $passText -AsPlainText -Force")
		fmt.Fprintln(stdin, "$creds = New-Object System.Management.Automation.PSCredential ($userName, $secpasswd)")
		fmt.Fprintln(stdin, "Connect-AzureAD -Credential $creds")
		fmt.Fprintln(stdin, "Get-AzureADDevice -All $true | Where {($_.DeviceTrustType -eq \"AzureAD\") -and ($_.ProfileType -eq \"RegisteredDevice\")} | Format-Table -Property DisplayName")
	}()

	if err = cmd.Start(); err != nil {
		writeError(fmt.Errorf("failed to connect to powershell: %w", err))
	}

	out, _ := io.ReadAll(stdout)
	errtxt, _ := io.ReadAll(stderr)

	if err = cmd.Wait(); err != nil {
		writeError(fmt.Errorf("failed to retrieve records from Azure: %w\n%s", err, errtxt))
	}

	//parse the powershell output
	psData := strings.Split(string(out[:]), "\r")
	skip := true
	count := 0
	for _, c := range psData {
		//Start of the computer records has been found, save each line until a blank line is encountered
		if !skip {
			trimmed := strings.TrimSpace((c))
			if trimmed == "" {
				break
			}
			adComputers = append(adComputers, strings.ToUpper(trimmed))
			count++
		} else {
			//Check if this line is the dashes right above the list of computers
			if strings.HasPrefix(strings.TrimSpace(c), "-----------") {
				skip = false
			}
		}
	}

	writeInfo(strconv.Itoa(count) + " records retrieved from Azure")
}

// Looking for items in dcComputers that don't exist in adComputers and aren't exempt in the config
func findComputersToRemoveFromDB() {
	count := 0
	for x := range dbComputers {
		found := false
		for y := range adComputers {
			if dbComputers[x] == adComputers[y] {
				found = true
				break
			}
		}

		if !found {
			for y := range config.Database.ExemptComputers {
				if dbComputers[x] == config.Database.ExemptComputers[y] {
					found = true
					writeInfo("Skipping " + dbComputers[x] + ", exempt from removal")
					break
				}
			}
		}

		if !found {
			if removeComputer(dbComputers[x]) {
				count++
			}
		}
	}

	writeInfo(strconv.Itoa(count) + " computers removed from database")
}

// Remove the record from the database
func removeComputer(name string) bool {
	conn, err := sql.Open("mssql", buildConnString())
	if err != nil {
		writeError(fmt.Errorf("database connection failed: %w", err))
	}
	defer conn.Close()

	_, err = conn.Exec("delete from Polaris.Workstations where ComputerName = ?", name)
	if err != nil {
		writeInfo(fmt.Sprintf("Failed to remove workstion %s: %s", name, err.Error()))
		return false
	} else {
		writeInfo(name + " removed from database")
	}

	return true
}
