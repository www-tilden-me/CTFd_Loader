#!/usr/bin/env python3

import os
import re
import json
import argparse
import requests
import warnings
from pathlib import Path
from dotenv import load_dotenv
from collections import defaultdict
from urllib.parse import unquote, urljoin, urlparse

SCORE_KEYS = [
    "id",
    "name",
    "ratings",
    "category",
    "value",
    "solved_by_me",
    "attempts",
]

META_KEYS = [
    "id",
    "name",
    "description",
    "category",
    "type",
    "files",
    "hints",
    "ratings",
]

SCORE_FILE_NAME = "RESULTS.json"
META_FILE_NAME = "META.json"

def sanitize_filename(value: str, fallback: str = "unnamed") -> str:
    """
    Convert an arbitrary challenge or file name into a filesystem-safe name.
    """
    value = unquote(str(value)).strip()
    value = value.replace("\\", "_").replace("/", "_")
    value = re.sub(r'[<>:"|?*\x00-\x1f]', "_", value)
    value = re.sub(r"\s+", " ", value).strip(" .")

    return value[:180] or fallback


def get_response(url, auth):
    response = requests.get(url, headers=auth, timeout=30)

    if response.status_code != 200:
        raise ValueError(
            f"Error getting request: "
            f"{response.status_code} - {response.text}"
        )

    try:
        res = response.json()
    except:
        raise ValueError("No response as json")

    if res.get("success") is False:
        raise ValueError(f"API returned success=false: {res}")

    return res


def get_challenges(base_url, auth):
    response = get_response(
        urljoin(base_url, "challenges"),
        auth,
    )
    return response["data"]


def get_challenge(challenge, base_url, auth):
    challenge_url = urljoin(
        base_url,
        f"challenges/{challenge['id']}",
    )

    response = get_response(challenge_url, auth)
    return response["data"]


def get_file(file_url, site_url, auth):
    """
    Download a CTFd challenge file.

    Returns:
        tuple[str, bytes]: filename and file contents
    """
    full_url = urljoin(site_url, file_url)

    response = requests.get(
        full_url,
        headers=auth,
        timeout=60,
    )

    if response.status_code != 200:
        raise ValueError(
            f"Error downloading {full_url}: "
            f"{response.status_code} - {response.text}"
        )

    # Ignore the query string, including the signed token.
    filename = Path(
        unquote(urlparse(full_url).path)
    ).name

    filename = sanitize_filename(filename, "downloaded_file")

    return filename, response.content


def save_challenge(challenge, location, site_url, auth):
    global META_KEYS
    meta = {
        key: value
        for key, value in challenge.items()
        if key in META_KEYS
    }

    meta["file_paths"] = meta.get("files", [])
    meta["files"] = []

    category = sanitize_filename(
        challenge.get("category", "UNKNOWN")
    ).replace(" ", "_")

    challenge_name = sanitize_filename(
        challenge.get("name", "UNKNOWN")
    ).replace(" ", "_")

    parent = Path(location) / category
    challenge_dir = parent / challenge_name

    challenge_dir.mkdir(parents=True, exist_ok=True)

    for file_url in challenge.get("files", []):
        filename, file_contents = get_file(
            file_url,
            site_url,
            auth,
        )

        with open(challenge_dir / filename, "wb") as f:
            f.write(file_contents)

        meta["files"].append(filename)

    with open(
        challenge_dir / META_FILE_NAME,
        "w",
        encoding="utf-8",
    ) as f:
        json.dump(meta, f, indent=2)

def format_score_output(challenges):
    grouped = {
        "SOLVED": defaultdict(list),
        "UNSOLVED": defaultdict(list),
    }

    for challenge in challenges:
        section = "SOLVED" if challenge.get("solved_by_me") else "UNSOLVED"
        rating = challenge.get("ratings", "UNRATED")

        grouped[section][rating].append(challenge)

    for section in ("SOLVED", "UNSOLVED"):
        print(f"{section}:")

        # Sort rating groups by the highest-point challenge in each group.
        ratings = sorted(
            grouped[section],
            key=lambda rating: max(
                challenge.get("value", 0)
                for challenge in grouped[section][rating]
            ),
            reverse=True,
        )

        for rating in ratings:
            print(f"    {rating}:")

            challenges_for_rating = sorted(
                grouped[section][rating],
                key=lambda challenge: challenge.get("value", 0),
                reverse=True,
            )

            for challenge in challenges_for_rating:
                name = challenge.get("name", "UNKNOWN")
                category = challenge.get("category", "UNKNOWN")
                value = challenge.get("value", 0)
                attempts = challenge.get("attempts", 0)

                print(
                    f"        {name}: {category} "
                    f"({value} pts, {attempts} tries)"
                )

        print()

def pull_event(site_url, location, auth):
    base_url = urljoin(site_url, "api/v1/")

    challenges = get_challenges(base_url, auth)
    failed = []
    for challenge in challenges:
        try:
            full_challenge = get_challenge(
                challenge,
                base_url,
                auth,
            )

            save_challenge(
                full_challenge,
                location,
                site_url,
                auth,
            )

            print(
                f"Saved: {full_challenge.get('name', 'UNKNOWN')}"
            )

        except Exception as e:
            failed.append(
                {
                    "id": challenge.get("id"),
                    "name": challenge.get("name"),
                    "err": str(e),
                }
            )

    if failed:
        print(f"Failed:\n{json.dumps(failed, indent=2)}")

def score_event(site_url, location, auth):
    global SCORE_KEYS
    base_url = urljoin(site_url, "api/v1/")


    challenges = get_challenges(base_url, auth)
    results = []
    failed = []
    for challenge in challenges:
        try:
            full_challenge = get_challenge(
                challenge,
                base_url,
                auth,
            )

            results.append({
                key: value
                for key, value in full_challenge.items()
                if key in SCORE_KEYS
            })

            print(
                f"Scored: {full_challenge.get('name', 'UNKNOWN')}"
            )

        except Exception as e:
            failed.append(
                {
                    "id": challenge.get("id"),
                    "name": challenge.get("name"),
                    "err": str(e),
                }
            )

    if failed:
        print(f"Failed:\n{json.dumps(failed, indent=2)}")

    with open(Path(location) / SCORE_FILE_NAME, "w") as f:
        json.dump(results, f, indent=2)
    
    format_score_output(results)

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ctfd.py",
        description="Pull and score challenges from a CTFd instance.",
    )

    subparsers = parser.add_subparsers(
        dest="command",
        required=True,
    )

    # Shared arguments for both pull and score
    def add_common_arguments(subparser: argparse.ArgumentParser) -> None:
        subparser.add_argument(
            "site_url",
            help="Base URL of the CTFd site",
        )
        subparser.add_argument(
            "location",
            help="Local challenge directory",
        )
        subparser.add_argument(
            "--session",
            dest="session_cookie",
            help="CTFd session cookie",
        )
        subparser.add_argument(
            "--auth",
            dest="auth_token",
            help="CTFd API authentication token",
        )

    pull_parser = subparsers.add_parser(
        "pull",
        help="Download challenges from a CTFd site",
    )
    add_common_arguments(pull_parser)

    score_parser = subparsers.add_parser(
        "score",
        help="Score event results",
    )
    add_common_arguments(score_parser)

    return parser

def main():
    parser = build_parser()

    args = parser.parse_args()

    load_dotenv()

    auth = dict()
    if args.auth_token:
        auth["Authorization"]= f"Token {args.auth_token}"
    elif os.getenv("CTFd_TOKEN"):
        auth["Authorization"]= f"Token {os.getenv('CTFd_TOKEN')}"
    
    if args.session_cookie:
        auth["Cookie"] = args.session_cookie

    if not auth:
        warnings.warn("No Authorization Provided")

    if args.command == "pull":
        pull_event(args.site_url, args.location, auth)
    elif args.command == "score":
        score_event(args.site_url, args.location, auth)

if __name__ == "__main__":
    main()