package docket

import "testing"

func TestDoctorProblemsCountsTheProblemsOfDoctorsJSON(t *testing.T) {
	n, err := DoctorProblems([]byte(`{"problems": [{"subject": "a", "problem": "x", "repair": "", "repaired": false},
		{"subject": "", "problem": "y", "repair": "z", "repaired": false}]}`))
	if err != nil || n != 2 {
		t.Fatalf("DoctorProblems = %d, %v; want 2, nil", n, err)
	}
	if n, err := DoctorProblems([]byte(`{"problems": []}`)); err != nil || n != 0 {
		t.Fatalf("no problems: DoctorProblems = %d, %v", n, err)
	}
	if _, err := DoctorProblems([]byte("usage: docket ...")); err == nil {
		t.Fatal("output that is not JSON gave no error")
	}
}

func TestDoctorNoticeSaysHowManyProblems(t *testing.T) {
	for n, want := range map[int]string{
		0: "",
		1: "docket doctor finds 1 problem in docket's files",
		3: "docket doctor finds 3 problems in docket's files",
	} {
		if got := DoctorNotice(n); got != want {
			t.Errorf("DoctorNotice(%d) = %q, want %q", n, got, want)
		}
	}
}
