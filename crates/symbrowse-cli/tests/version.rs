#![deny(unsafe_code)]

use std::process::{Command, Output};

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_symbrowse"))
        .args(args)
        .output()
        .expect("run symbrowse")
}

#[test]
fn version_text_matches_go_contract() {
    let output = run(&["version"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symbrowse dev\n");
    assert!(output.stderr.is_empty());
}

#[test]
fn structured_version_modes_match_go_contract() {
    for args in [
        &["--json", "version"][..],
        &["version", "--json"][..],
        &["version", "--output", "json"][..],
        &["--output=json", "version"][..],
        &["version", "--output", "yaml"][..],
    ] {
        let output = run(args);
        assert_eq!(output.status.code(), Some(0), "args={args:?}");
        assert_eq!(
            output.stdout, b"{\"tool\":\"symbrowse\",\"version\":\"dev\",\"schema_version\":8}\n",
            "args={args:?}"
        );
        assert!(output.stderr.is_empty(), "args={args:?}");
    }
}

#[test]
fn root_version_flags_match_go_contract() {
    for args in [&["-v"][..], &["--version"][..]] {
        let output = run(args);
        assert_eq!(output.status.code(), Some(0));
        assert_eq!(output.stdout, b"symbrowse version dev\n");
        assert!(output.stderr.is_empty());
    }
}

#[test]
fn invalid_output_and_extra_arguments_match_go_contract() {
    let output = run(&["version", "--output", "wat"]);
    assert_eq!(output.status.code(), Some(2));
    assert!(output.stdout.is_empty());
    assert_eq!(
        output.stderr,
        b"invalid --output format \"wat\": want text, json or yaml\n"
    );

    for args in [&["version", "extra"][..], &["version", "--", "--json"][..]] {
        let output = run(args);
        assert_eq!(output.status.code(), Some(2));
        assert!(output.stdout.is_empty());
        let argument = if args[1] == "--" { "--json" } else { args[1] };
        assert_eq!(
            output.stderr,
            format!("unknown command {argument:?} for \"symbrowse version\"\n").as_bytes()
        );
    }
}
