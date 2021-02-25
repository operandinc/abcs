#!/bin/bash

 recipient="${1}"
 message="${*:2}"
 cat<<EOF | osascript - "${recipient}" "${message}"
 on run {targetBuddyPhone, targetMessage}
    tell application "Messages"
        set targetService to 1st service whose service type = iMessage
        set targetBuddy to buddy targetBuddyPhone of targetService
        send targetMessage to targetBuddy
    end tell
end run
EOF
