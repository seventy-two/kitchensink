package io.github.seventytwo.kitchensink.service

import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import io.github.seventytwo.kitchensink.KitchensinkApp
import io.github.seventytwo.kitchensink.MainActivity
import io.github.seventytwo.kitchensink.R
import io.github.seventytwo.kitchensink.notify.Notifier

/**
 * Keeps the process, and so the event stream, alive while the app's in
 * the background, so notifications arrive as things happen. Android needs
 * a notification showing for it; it's in a silent, minimised channel.
 */
class LiveService : Service() {
    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE,
        )
        val n = NotificationCompat.Builder(this, Notifier.CHANNEL_LIVE)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle("Watching for events")
            .setContentIntent(open)
            .setOngoing(true)
            .setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_MIN)
            .build()
        val type = if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0
        try {
            ServiceCompat.startForeground(this, Notifier.LIVE_ID, n, type)
        } catch (_: Exception) {
            // Not allowed from here (e.g. started in the background on a
            // newer Android); the stream still runs while the app's open.
            stopSelf()
            return START_NOT_STICKY
        }
        (application as KitchensinkApp).hub.ensureStream()
        return START_STICKY
    }

    companion object {
        fun start(context: Context) {
            try {
                context.startForegroundService(Intent(context, LiveService::class.java))
            } catch (_: Exception) {
            }
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, LiveService::class.java))
        }
    }
}

/** Starts the background connection after the phone restarts, if it's wanted. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val hub = (context.applicationContext as KitchensinkApp).hub
        val c = hub.config.value
        if (c.background && c.configured) LiveService.start(context)
    }
}
