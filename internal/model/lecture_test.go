package model

import "testing"

func TestIsLecture(t *testing.T) {
	t.Parallel()
	if !IsLecture("Лекция") || !IsLecture("лекция") || !IsLecture("Lecture") {
		t.Fatal("lecture types")
	}
	if IsLecture("Практика") || IsLecture("Лабораторная") || IsLecture("unknown") || IsLecture("") {
		t.Fatal("non-lecture")
	}
}
