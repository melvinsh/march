// march — a bare SDDM greeter over the march background.
//
// Autologin means this screen is usually only ever the frozen moment between
// the kernel's boot log and Hyprland's first frame; when autologin is off it
// is a working login form. Either way the one thing it must never be is black:
// the window belongs to the guest, and "something is starting" is the loader.
import QtQuick 2.15
import SddmComponents 2.0

Rectangle {
    id: root
    width: 640
    height: 480
    color: "#0a0a16"

    Image {
        anchors.fill: parent
        source: "/usr/share/backgrounds/march.png"
        fillMode: Image.PreserveAspectCrop
    }

    Column {
        anchors.centerIn: parent
        spacing: 18

        Text {
            text: "march"
            anchors.horizontalCenter: parent.horizontalCenter
            color: "#eae6ff"
            font.pixelSize: 42
            font.weight: Font.DemiBold
        }

        Text {
            text: "Arch Linux ARM"
            anchors.horizontalCenter: parent.horizontalCenter
            color: "#9b95b0"
            font.pixelSize: 14
        }

        Item { width: 1; height: 8 }

        TextBox {
            id: userBox
            anchors.horizontalCenter: parent.horizontalCenter
            width: 260
            height: 34
            text: ""
        }

        PasswordBox {
            id: passBox
            anchors.horizontalCenter: parent.horizontalCenter
            width: 260
            height: 34
            text: ""
            focus: true
            Keys.onReturnPressed: login()
        }

        Button {
            id: loginButton
            anchors.horizontalCenter: parent.horizontalCenter
            text: "Sign in"
            onClicked: login()
        }
    }
}