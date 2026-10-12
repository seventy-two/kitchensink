package io.github.seventytwo.kitchensink.notify

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import io.github.seventytwo.kitchensink.MainActivity
import io.github.seventytwo.kitchensink.R
import io.github.seventytwo.kitchensink.model.Event
import io.github.seventytwo.kitchensink.model.Priority

/**
 * Raises a notification for each new event. They're held briefly so a
 * burst, e.g. after being offline, becomes one notification.
 */
class Notifier(private val context: Context) {
    private val manager = NotificationManagerCompat.from(context)
    private val handler = Handler(Looper.getMainLooper())
    private val pending = mutableListOf<Event>()
    private var titleOf: (String) -> String = { it }

    init {
        createChannels(context)
    }

    /** Queues an event; what's queued is shown 1.5 seconds after the first. */
    fun hold(e: Event, titleOf: (String) -> String) {
        this.titleOf = titleOf
        if (pending.isEmpty()) handler.postDelayed(::flush, HOLD_MS)
        pending += e
    }

    /** Removes the event notifications, once the dashboard's in front of the user. */
    fun clearEvents() {
        manager.cancel(EVENT_ID)
    }

    private fun flush() {
        if (pending.isEmpty()) return
        val events = pending.toList()
        pending.clear()
        if (ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED &&
            Build.VERSION.SDK_INT >= 33
        ) return

        val high = events.any { it.priority == Priority.High }
        val b = NotificationCompat.Builder(context, if (high) CHANNEL_HIGH else CHANNEL_EVENTS)
            .setSmallIcon(R.drawable.ic_notification)
            .setAutoCancel(true)
            .setCategory(NotificationCompat.CATEGORY_EVENT)
        if (events.size == 1) {
            val e = events[0]
            val body = e.body.ifEmpty { titleOf(e.monitor) }
            b.setContentTitle(e.title).setContentText(body).setStyle(NotificationCompat.BigTextStyle().bigText(body))
            b.setContentIntent(open(e.url))
        } else {
            // Several at once: one summary, newest first.
            val titles = events.reversed().map { it.title }
            val style = NotificationCompat.InboxStyle()
            titles.forEach { style.addLine(it) }
            b.setContentTitle("${events.size} new events").setContentText(titles.first()).setStyle(style)
            b.setContentIntent(open(""))
        }
        try {
            manager.notify(EVENT_ID, b.build())
        } catch (_: SecurityException) {
        }
    }

    /** Opens an event's link, or the dashboard if it has none. */
    private fun open(url: String): PendingIntent {
        val intent = if (url.startsWith("https://") || url.startsWith("http://")) {
            Intent(Intent.ACTION_VIEW, Uri.parse(url))
        } else {
            Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP)
        }
        return PendingIntent.getActivity(context, url.hashCode(), intent, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    companion object {
        const val CHANNEL_EVENTS = "events"
        const val CHANNEL_HIGH = "high"
        const val CHANNEL_LIVE = "live"
        const val EVENT_ID = 1
        const val LIVE_ID = 2
        private const val HOLD_MS = 1500L

        fun createChannels(context: Context) {
            val nm = context.getSystemService(NotificationManager::class.java)
            nm.createNotificationChannels(
                listOf(
                    NotificationChannel(CHANNEL_EVENTS, "Events", NotificationManager.IMPORTANCE_DEFAULT).apply {
                        description = "What your monitors notice: scores, plays, price moves, reminders"
                    },
                    NotificationChannel(CHANNEL_HIGH, "High-priority events", NotificationManager.IMPORTANCE_HIGH).apply {
                        description = "Events the server marks high priority"
                    },
                    NotificationChannel(CHANNEL_LIVE, "Staying connected", NotificationManager.IMPORTANCE_MIN).apply {
                        description = "Shown while kitchensink stays connected in the background"
                        setShowBadge(false)
                    },
                ),
            )
        }
    }
}
