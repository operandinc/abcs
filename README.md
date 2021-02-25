**ABCS** is a simple HTTP server which can be used to send and receive iMessages
programmatically on a machine running macOS. ABCS totally isn't an acronym for
"Apple Business Chat Sucks". The great thing about this approach is that you
don't need a human on the other end, making it an excellent tool for bots and
virtual assistants.

The tool itself is super simple. Whenever a new iMessage is received, it will post
a small JSON blob to an endpoint of your choosing. When you want to send a message,
you can configure a listening address (defaults to `localhost:11106`) and hit the
root endpoint with a small JSON blob of the message you want to send, and to whom.

**Example Usage**

To build the tool, just do `go build .` in the project folder.

`./abcs -endpoint=https://example.com/cool_endpoint -listen=127.0.0.1:8080`

This command will start a web server on `127.0.0.1:8080` listening for requests.
If you send a JSON blob like the following to this endpoint, the iMessage will
be sent.

```
{
    "to":"1234567890",
    "message":"ABCS is not an acronym. Remember this."
}
```

When you receive an iMessage, a JSON blob like the following will be `POST`ed to
the endpoint `https://example.com/cool_endpoint`.

```
{
    "from":"1234567890",
    "message":"Yeah, I get it. ABCS isn't an acronym."
}
```

**Other Stuff**

This tool is inherently insecure, since there is no authentication baked in. Rather,
it is reccomended to use a VPC or some sort of private network (I recommend [Tailscale](https://tailscale.com)) to ensure that this server exposed publicly. This tool is used
in production for [Operand](https://operand.ai), though if reliability is important
I'd reccomend building some additional stuff on top of this. It would be great
to eventually have multiple hosted Mac minis and automatically detect machine failures,
all that jazz.

Tools shouldn't limit, they should empower. We at Operand disagree that a human
should be required on the other end of an iMessage conversation with a business.
But of course, ABCS isn't an acronym for "Apple Business Chat Sucks" and in no
way intends to disparage a product of another company.

Have a beautiful day <3.

P.S. Big thanks to https://github.com/golift/imessage, I spent a bunch of time
last night figuring out the iMessage stuff myself, only to discover that someone
had done it really well and open sourced it as a tool. This tool is simply a wrapper
of this existing work, and adds server functionality.
