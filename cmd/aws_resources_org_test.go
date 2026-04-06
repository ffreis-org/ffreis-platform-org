package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
)

const (
	testOrganizationAccountName       = "test-account"
	testOrganizationEnvOUID           = "ou-0001-abc12345"
	testOrganizationsHeaderTarget     = "X-Amz-Target"
	testOrganizationsListRoots        = "AWSOrganizationsV20161128.ListRoots"
	testOrganizationsListOUsForParent = "AWSOrganizationsV20161128.ListOrganizationalUnitsForParent"
	testOrganizationsListPolicies     = "AWSOrganizationsV20161128.ListPolicies"
	testOrganizationsListAccounts     = "AWSOrganizationsV20161128.ListAccounts"
	testOrganizationsDetachPolicy     = "AWSOrganizationsV20161128.DetachPolicy"
	testOrganizationsDeletePolicy     = "AWSOrganizationsV20161128.DeletePolicy"
	testOrganizationsDeleteOU         = "AWSOrganizationsV20161128.DeleteOrganizationalUnit"
	testOrganizationsCloseAccount     = "AWSOrganizationsV20161128.CloseAccount"
)

func testOrganizationsClient(t *testing.T, handler http.HandlerFunc) *organizations.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return organizations.New(organizations.Options{
		Region:       testRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIA", "secret", "token"),
		BaseEndpoint: sdkaws.String(server.URL),
		HTTPClient:   server.Client(),
	})
}

func overrideOrganizationsClient(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	old := newOrganizationsClient
	t.Cleanup(func() { newOrganizationsClient = old })
	newOrganizationsClient = func(_ sdkaws.Config, _ ...func(*organizations.Options)) *organizations.Client {
		return testOrganizationsClient(t, handler)
	}
}

// --- findOrganizationalUnitIDByName ---

func TestFindOrganizationalUnitIDByNameListRootsError(t *testing.T) {
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"__type":"ServiceException","message":"internal error"}`)
	})
	_, err := findOrganizationalUnitIDByName(context.Background(), client, "environments")
	if err == nil {
		t.Fatal("expected error from ListRoots failure")
	}
}

func TestFindOrganizationalUnitIDByNameFound(t *testing.T) {
	call := 0
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		call++
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:aws:organizations::123:root/o-abc/r-0001"}]}`)
		case target == testOrganizationsListOUsForParent:
			_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"`+testOrganizationEnvOUID+`","Name":"environments","Arn":"arn:..."}]}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	id, err := findOrganizationalUnitIDByName(context.Background(), client, "environments")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != testOrganizationEnvOUID {
		t.Fatalf("expected %s, got %q", testOrganizationEnvOUID, id)
	}
}

func TestFindOrganizationalUnitIDByNameNotFound(t *testing.T) {
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:..."}]}`)
		case target == testOrganizationsListOUsForParent:
			_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"ou-0001-xyz","Name":"other"}]}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	id, err := findOrganizationalUnitIDByName(context.Background(), client, "environments")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != "" {
		t.Fatalf("expected empty string for not-found OU, got %q", id)
	}
}

func TestFindOrganizationalUnitIDByNamePaginated(t *testing.T) {
	listCalls := 0
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:..."}]}`)
		case target == testOrganizationsListOUsForParent:
			listCalls++
			if listCalls == 1 {
				_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"ou-first","Name":"other"}],"NextToken":"page2"}`)
			} else {
				_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"`+testOrganizationEnvOUID+`","Name":"environments"}]}`)
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	id, err := findOrganizationalUnitIDByName(context.Background(), client, "environments")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != testOrganizationEnvOUID {
		t.Fatalf("expected %s, got %q", testOrganizationEnvOUID, id)
	}
	if listCalls != 2 {
		t.Fatalf("expected 2 list calls, got %d", listCalls)
	}
}

// --- findOrganizationAccountIDByName ---

func TestFindOrganizationAccountIDByNameFound(t *testing.T) {
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		_, _ = io.WriteString(w, `{"Accounts":[{"Id":"111122223333","Name":"`+testOrganizationAccountName+`","Status":"ACTIVE"}]}`)
	})
	id, err := findOrganizationAccountIDByName(context.Background(), client, testOrganizationAccountName)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != "111122223333" {
		t.Fatalf("expected 111122223333, got %q", id)
	}
}

func TestFindOrganizationAccountIDByNameNotFound(t *testing.T) {
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		_, _ = io.WriteString(w, `{"Accounts":[{"Id":"111122223333","Name":"other-account","Status":"ACTIVE"}]}`)
	})
	id, err := findOrganizationAccountIDByName(context.Background(), client, testOrganizationAccountName)
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != "" {
		t.Fatalf("expected empty string for not-found account, got %q", id)
	}
}

func TestFindOrganizationAccountIDByNameError(t *testing.T) {
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"__type":"ServiceException","message":"internal error"}`)
	})
	_, err := findOrganizationAccountIDByName(context.Background(), client, testOrganizationAccountName)
	if err == nil {
		t.Fatal("expected error from ListAccounts failure")
	}
}

// --- findOrganizationTargetIDByName ---

func TestFindOrganizationTargetIDByNameEnvironmentsOU(t *testing.T) {
	// "environments" goes through findOrganizationalUnitIDByName path
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:..."}]}`)
		case target == testOrganizationsListOUsForParent:
			_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"`+testOrganizationEnvOUID+`","Name":"environments"}]}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	id, err := findOrganizationTargetIDByName(context.Background(), client, "environments")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != testOrganizationEnvOUID {
		t.Fatalf("expected %s, got %q", testOrganizationEnvOUID, id)
	}
}

func TestFindOrganizationTargetIDByNameAccountName(t *testing.T) {
	// Non-"environments" name goes through findOrganizationAccountIDByName
	client := testOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		_, _ = io.WriteString(w, `{"Accounts":[{"Id":"111122223333","Name":"my-account","Status":"ACTIVE"}]}`)
	})
	id, err := findOrganizationTargetIDByName(context.Background(), client, "my-account")
	if err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
	if id != "111122223333" {
		t.Fatalf("expected 111122223333, got %q", id)
	}
}

// --- detachOrganizationPolicyBySyntheticName ---

func TestDetachOrganizationPolicyBySyntheticNameInvalidFormat(t *testing.T) {
	if err := detachOrganizationPolicyBySyntheticName(context.Background(), "no-at-sign"); err == nil {
		t.Fatal("expected error for invalid synthetic name")
	}
}

func TestDetachOrganizationPolicyBySyntheticNamePolicyNotFound(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		// ListPolicies → empty
		_, _ = io.WriteString(w, `{"Policies":[]}`)
	})
	if err := detachOrganizationPolicyBySyntheticName(context.Background(), "missing-policy@my-account"); err != nil {
		t.Fatalf("expected nil when policy not found, got: %v", err)
	}
}

func TestDetachOrganizationPolicyBySyntheticNameTargetNotFound(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListPolicies:
			_, _ = io.WriteString(w, `{"Policies":[{"Id":"p-abc123","Name":"my-policy","Arn":"arn:..."}]}`)
		case target == testOrganizationsListAccounts:
			// target name not found in accounts
			_, _ = io.WriteString(w, `{"Accounts":[]}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := detachOrganizationPolicyBySyntheticName(context.Background(), "my-policy@missing-account"); err != nil {
		t.Fatalf("expected nil when target not found, got: %v", err)
	}
}

func TestDetachOrganizationPolicyBySyntheticNameSuccess(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListPolicies:
			_, _ = io.WriteString(w, `{"Policies":[{"Id":"p-abc123","Name":"my-policy","Arn":"arn:..."}]}`)
		case target == testOrganizationsListAccounts:
			_, _ = io.WriteString(w, `{"Accounts":[{"Id":"111122223333","Name":"my-account","Status":"ACTIVE"}]}`)
		case target == testOrganizationsDetachPolicy:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := detachOrganizationPolicyBySyntheticName(context.Background(), "my-policy@my-account"); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
}

// --- deleteOrganizationPolicyByName ---

func TestDeleteOrganizationPolicyByNameNotFound(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		_, _ = io.WriteString(w, `{"Policies":[]}`)
	})
	if err := deleteOrganizationPolicyByName(context.Background(), "missing"); err != nil {
		t.Fatalf("expected nil when policy not found, got: %v", err)
	}
}

func TestDeleteOrganizationPolicyByNameSuccess(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListPolicies:
			_, _ = io.WriteString(w, `{"Policies":[{"Id":"p-abc123","Name":"my-policy","Arn":"arn:..."}]}`)
		case target == testOrganizationsDeletePolicy:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := deleteOrganizationPolicyByName(context.Background(), "my-policy"); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
}

// --- deleteOrganizationOUByName ---

func TestDeleteOrganizationOUByNameNotFound(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:..."}]}`)
		case target == testOrganizationsListOUsForParent:
			_, _ = io.WriteString(w, `{"OrganizationalUnits":[]}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := deleteOrganizationOUByName(context.Background(), "missing-ou"); err != nil {
		t.Fatalf("expected nil when OU not found, got: %v", err)
	}
}

func TestDeleteOrganizationOUByNameSuccess(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListRoots:
			_, _ = io.WriteString(w, `{"Roots":[{"Id":"r-0001","Name":"Root","Arn":"arn:..."}]}`)
		case target == testOrganizationsListOUsForParent:
			_, _ = io.WriteString(w, `{"OrganizationalUnits":[{"Id":"`+testOrganizationEnvOUID+`","Name":"environments"}]}`)
		case target == testOrganizationsDeleteOU:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := deleteOrganizationOUByName(context.Background(), "environments"); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
}

// --- closeOrganizationAccountByName ---

func TestCloseOrganizationAccountByNameNotFound(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		_, _ = io.WriteString(w, `{"Accounts":[]}`)
	})
	if err := closeOrganizationAccountByName(context.Background(), "missing"); err != nil {
		t.Fatalf("expected nil when account not found, got: %v", err)
	}
}

func TestCloseOrganizationAccountByNameSuccess(t *testing.T) {
	overrideOrganizationsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(testHTTPHeaderContentType, testHTTPContentTypeAMZJSON11)
		target := r.Header.Get(testOrganizationsHeaderTarget)
		switch {
		case target == testOrganizationsListAccounts:
			_, _ = io.WriteString(w, `{"Accounts":[{"Id":"111122223333","Name":"my-account","Status":"ACTIVE"}]}`)
		case target == testOrganizationsCloseAccount:
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	if err := closeOrganizationAccountByName(context.Background(), "my-account"); err != nil {
		t.Fatalf(errUnexpectedError, err)
	}
}

// Ensure fmt is used.
var _ = fmt.Sprintf
