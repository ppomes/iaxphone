# The stock image has no sound files: this adds the English core sounds in
# u-law, so Playback() works (602, and the "not in service" prompt on an
# unknown number).
FROM andrius/asterisk

ARG SOUNDS_VERSION=1.6.1
RUN mkdir -p /var/lib/asterisk/sounds/en \
    && curl -fsSL https://downloads.asterisk.org/pub/telephony/sounds/releases/asterisk-core-sounds-en-ulaw-${SOUNDS_VERSION}.tar.gz \
       | tar xz -C /var/lib/asterisk/sounds/en
